package service

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/setting"
	"github.com/google/uuid"
)

const wechatPayHost = "https://api.mch.weixin.qq.com"

type wechatPayClient struct {
	mchId        string
	appId        string
	certSerial   string
	privateKey   *rsa.PrivateKey
	platformCert *x509.Certificate
	certMu       sync.Mutex
}

func NewWechatPayClient() (*wechatPayClient, error) {
	if setting.WechatMchId == "" || setting.WechatCertSerial == "" || setting.WechatPrivateKey == "" {
		return nil, errors.New("微信支付未配置完整（mchid/证书序列号/商户私钥）")
	}
	privateKey, err := parsePrivateKey(setting.WechatPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("解析微信商户私钥失败: %w", err)
	}
	client := &wechatPayClient{
		mchId:      setting.WechatMchId,
		appId:      setting.WechatAppId,
		certSerial: setting.WechatCertSerial,
		privateKey: privateKey,
	}
	if setting.WechatPlatformCert != "" {
		cert, err := parseCertificate(setting.WechatPlatformCert)
		if err == nil {
			client.platformCert = cert
		}
	}
	return client, nil
}

func parsePrivateKey(pemStr string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("PEM 解码失败")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, errors.New("无法解析 RSA 私钥")
}

func parseCertificate(pemStr string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(pemStr))
	if block == nil {
		return nil, errors.New("PEM 解码失败")
	}
	return x509.ParseCertificate(block.Bytes)
}

func (c *wechatPayClient) doRequest(method, urlPath, body string) ([]byte, error) {
	authorization, err := c.sign(method, urlPath, body)
	if err != nil {
		return nil, err
	}
	var reqBody io.Reader
	if body != "" {
		reqBody = bytes.NewReader([]byte(body))
	}
	req, err := http.NewRequest(method, wechatPayHost+urlPath, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "new-api")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("微信支付请求失败 method=%s path=%s status=%d body=%s", method, urlPath, resp.StatusCode, string(respBody))
	}
	return respBody, nil
}

type wechatCertificateResponse struct {
	Data []struct {
		SerialNo           string `json:"serial_no"`
		EncryptCertificate struct {
			Algorithm      string `json:"algorithm"`
			Nonce          string `json:"nonce"`
			AssociatedData string `json:"associated_data"`
			Ciphertext     string `json:"ciphertext"`
		} `json:"encrypt_certificate"`
	} `json:"data"`
}

func (c *wechatPayClient) fetchPlatformCert() error {
	respBody, err := c.doRequest(http.MethodGet, "/v3/certificates", "")
	if err != nil {
		return err
	}
	var certResp wechatCertificateResponse
	if err := json.Unmarshal(respBody, &certResp); err != nil {
		return err
	}
	if len(certResp.Data) == 0 {
		return errors.New("微信平台证书列表为空")
	}

	enc := certResp.Data[0].EncryptCertificate
	ciphertext, err := base64.StdEncoding.DecodeString(enc.Ciphertext)
	if err != nil {
		return err
	}
	block, err := aes.NewCipher([]byte(setting.WechatApiV3Key))
	if err != nil {
		return fmt.Errorf("API v3 密钥无效: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	plaintext, err := gcm.Open(nil, []byte(enc.Nonce), ciphertext, []byte(enc.AssociatedData))
	if err != nil {
		return fmt.Errorf("平台证书解密失败: %w", err)
	}

	cert, err := parseCertificate(string(plaintext))
	if err != nil {
		return fmt.Errorf("解析平台证书失败: %w", err)
	}
	c.platformCert = cert
	return nil
}

func (c *wechatPayClient) ensurePlatformCert() error {
	if c.platformCert != nil {
		return nil
	}
	c.certMu.Lock()
	defer c.certMu.Unlock()
	if c.platformCert != nil {
		return nil
	}
	return c.fetchPlatformCert()
}

func (c *wechatPayClient) sign(method, urlPath, body string) (authorization string, err error) {
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	nonce := uuid.NewString()
	message := fmt.Sprintf("%s\n%s\n%s\n%s\n%s\n", method, urlPath, timestamp, nonce, body)
	hashed := sha256.Sum256([]byte(message))
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.privateKey, crypto.SHA256, hashed[:])
	if err != nil {
		return "", err
	}
	signatureStr := base64.StdEncoding.EncodeToString(signature)
	authorization = fmt.Sprintf(
		`WECHATPAY2-SHA256-RSA2048 mchid="%s",nonce_str="%s",signature="%s",timestamp="%s",serial_no="%s"`,
		c.mchId, nonce, signatureStr, timestamp, c.certSerial,
	)
	return authorization, nil
}

type wechatNativeOrderRequest struct {
	AppId       string `json:"appid"`
	MchId       string `json:"mchid"`
	Description string `json:"description"`
	OutTradeNo  string `json:"out_trade_no"`
	NotifyUrl   string `json:"notify_url"`
	Amount      struct {
		Total    int    `json:"total"`
		Currency string `json:"currency"`
	} `json:"amount"`
}

type wechatNativeOrderResponse struct {
	CodeUrl string `json:"code_url"`
}

func (c *wechatPayClient) CreateNativeOrder(description, outTradeNo, notifyUrl string, amountFen int) (codeUrl string, err error) {
	reqBody := wechatNativeOrderRequest{
		AppId:       c.appId,
		MchId:       c.mchId,
		Description: description,
		OutTradeNo:  outTradeNo,
		NotifyUrl:   notifyUrl,
	}
	reqBody.Amount.Total = amountFen
	reqBody.Amount.Currency = "CNY"

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	urlPath := "/v3/pay/transactions/native"
	authorization, err := c.sign(http.MethodPost, urlPath, string(bodyBytes))
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest(http.MethodPost, wechatPayHost+urlPath, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "new-api")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("微信支付下单失败 status=%d body=%s", resp.StatusCode, string(respBody))
	}

	var orderResp wechatNativeOrderResponse
	if err := json.Unmarshal(respBody, &orderResp); err != nil {
		return "", err
	}
	if orderResp.CodeUrl == "" {
		return "", errors.New("微信支付下单响应缺少 code_url")
	}
	return orderResp.CodeUrl, nil
}

type wechatNotifyResource struct {
	Algorithm      string `json:"algorithm"`
	Ciphertext     string `json:"ciphertext"`
	AssociatedData string `json:"associated_data"`
	Nonce          string `json:"nonce"`
}

type wechatNotifyBody struct {
	Id           string              `json:"id"`
	CreateTime   string              `json:"create_time"`
	EventType    string              `json:"event_type"`
	ResourceType string              `json:"resource_type"`
	Resource     wechatNotifyResource `json:"resource"`
}

type wechatNotifyTransaction struct {
	OutTradeNo     string `json:"out_trade_no"`
	TradeState     string `json:"trade_state"`
	TransactionId  string `json:"transaction_id"`
	SuccessTime    string `json:"success_time"`
	Amount         struct {
		Total    int    `json:"total"`
		Currency string `json:"currency"`
	} `json:"amount"`
}

func (c *wechatPayClient) VerifyAndDecryptNotify(body []byte, timestamp, nonce, signature string) (*wechatNotifyTransaction, error) {
	if err := c.ensurePlatformCert(); err != nil {
		return nil, fmt.Errorf("获取微信平台证书失败: %w", err)
	}
	sigBytes, err := base64.StdEncoding.DecodeString(signature)
	if err != nil {
		return nil, errors.New("回调签名 base64 解码失败")
	}
	message := fmt.Sprintf("%s\n%s\n%s\n", timestamp, nonce, string(body))
	hashed := sha256.Sum256([]byte(message))
	if err := rsa.VerifyPKCS1v15(c.platformCert.PublicKey.(*rsa.PublicKey), crypto.SHA256, hashed[:], sigBytes); err != nil {
		return nil, errors.New("回调验签失败")
	}

	var notify wechatNotifyBody
	if err := json.Unmarshal(body, &notify); err != nil {
		return nil, fmt.Errorf("回调请求体解析失败: %w", err)
	}

	ciphertext, err := base64.StdEncoding.DecodeString(notify.Resource.Ciphertext)
	if err != nil {
		return nil, errors.New("回调密文 base64 解码失败")
	}
	key := []byte(setting.WechatApiV3Key)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("API v3 密钥无效")
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	plaintext, err := gcm.Open(nil, []byte(notify.Resource.Nonce), ciphertext, []byte(notify.Resource.AssociatedData))
	if err != nil {
		return nil, errors.New("回调解密失败")
	}

	var transaction wechatNotifyTransaction
	if err := json.Unmarshal(plaintext, &transaction); err != nil {
		return nil, fmt.Errorf("回调交易数据解析失败: %w", err)
	}
	return &transaction, nil
}
