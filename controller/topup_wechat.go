package controller

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

func isWechatPayEnabled() bool {
	return setting.WechatPayEnabled
}

type WechatPayRequest struct {
	Amount int64 `json:"amount"`
}

func RequestWechatPay(c *gin.Context) {
	if !isWechatPayEnabled() {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "微信支付未启用"})
		return
	}

	var req WechatPayRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "参数错误"})
		return
	}
	if req.Amount < getMinTopup() {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": fmt.Sprintf("充值数量不能小于 %d", getMinTopup())})
		return
	}
	id := c.GetInt("id")
	if rejectInvalidTopUpQuota(c, id, req.Amount) {
		return
	}

	group, err := model.GetUserGroup(id, true)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "获取用户分组失败"})
		return
	}
	payMoney := getPayMoney(req.Amount, group)
	if payMoney < 0.01 {
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "充值金额过低"})
		return
	}

	client, err := service.NewWechatPayClient()
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("微信支付 客户端初始化失败 user_id=%d error=%q", id, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "当前管理员未配置微信支付信息"})
		return
	}

	tradeNo := fmt.Sprintf("USR%dNOWX%s%d", id, common.GetRandomString(6), time.Now().Unix())
	callBackAddress := service.GetCallbackAddress()
	notifyUrl := callBackAddress + "/api/user/wechat/notify"

	amountFen := decimal.NewFromFloat(payMoney).Mul(decimal.NewFromInt(100)).IntPart()
	description := fmt.Sprintf("充值 %d", req.Amount)

	codeUrl, err := client.CreateNativeOrder(description, tradeNo, notifyUrl, int(amountFen))
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("微信支付 拉起支付失败 user_id=%d trade_no=%s amount=%d error=%q", id, tradeNo, req.Amount, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "拉起支付失败"})
		return
	}

	amount := req.Amount
	if operation_setting.GetQuotaDisplayType() == operation_setting.QuotaDisplayTypeTokens {
		dAmount := decimal.NewFromInt(int64(amount))
		dQuotaPerUnit := decimal.NewFromFloat(common.QuotaPerUnit)
		amount = dAmount.Div(dQuotaPerUnit).IntPart()
	}

	topUp := &model.TopUp{
		UserId:          id,
		Amount:          amount,
		Money:           payMoney,
		TradeNo:         tradeNo,
		PaymentMethod:   model.PaymentMethodWechat,
		PaymentProvider: model.PaymentProviderWechat,
		CreateTime:      time.Now().Unix(),
		Status:          common.TopUpStatusPending,
	}
	if err := topUp.Insert(); err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("微信支付 创建充值订单失败 user_id=%d trade_no=%s amount=%d error=%q", id, tradeNo, req.Amount, err.Error()))
		c.JSON(http.StatusOK, gin.H{"message": "error", "data": "创建订单失败"})
		return
	}

	logger.LogInfo(c.Request.Context(), fmt.Sprintf("微信支付 充值订单创建成功 user_id=%d trade_no=%s amount=%d money=%.2f", id, tradeNo, req.Amount, payMoney))
	c.JSON(http.StatusOK, gin.H{"message": "success", "data": gin.H{"code_url": codeUrl, "trade_no": tradeNo}})
}

func WechatNotify(c *gin.Context) {
	if !isWechatPayEnabled() {
		c.JSON(http.StatusOK, gin.H{"code": "FAIL", "message": "微信支付未启用"})
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"code": "FAIL", "message": "读取请求体失败"})
		return
	}

	timestamp := c.GetHeader("Wechatpay-Timestamp")
	nonce := c.GetHeader("Wechatpay-Nonce")
	signature := c.GetHeader("Wechatpay-Signature")

	client, err := service.NewWechatPayClient()
	if err != nil {
		logger.LogError(c.Request.Context(), fmt.Sprintf("微信支付 回调客户端初始化失败 client_ip=%s error=%q", c.ClientIP(), err.Error()))
		c.JSON(http.StatusOK, gin.H{"code": "FAIL", "message": "客户端初始化失败"})
		return
	}

	transaction, err := client.VerifyAndDecryptNotify(body, timestamp, nonce, signature)
	if err != nil {
		logger.LogWarn(c.Request.Context(), fmt.Sprintf("微信支付 回调验签/解密失败 client_ip=%s error=%q", c.ClientIP(), err.Error()))
		c.JSON(http.StatusOK, gin.H{"code": "FAIL", "message": "验签失败"})
		return
	}

	if transaction.TradeState != "SUCCESS" {
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("微信支付 回调忽略非成功事件 trade_no=%s trade_state=%s client_ip=%s", transaction.OutTradeNo, transaction.TradeState, c.ClientIP()))
		c.JSON(http.StatusOK, gin.H{"code": "SUCCESS", "message": "成功"})
		return
	}

	LockOrder(transaction.OutTradeNo)
	defer UnlockOrder(transaction.OutTradeNo)

	alreadyDone, err := model.RechargeWechat(transaction.OutTradeNo, c.ClientIP())
	if err != nil {
		switch {
		case errors.Is(err, model.ErrTopUpNotFound):
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("微信支付 回调订单不存在 trade_no=%s client_ip=%s", transaction.OutTradeNo, c.ClientIP()))
		case errors.Is(err, model.ErrPaymentMethodMismatch):
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("微信支付 订单支付网关不匹配 trade_no=%s client_ip=%s", transaction.OutTradeNo, c.ClientIP()))
		case errors.Is(err, model.ErrTopUpStatusInvalid):
			logger.LogWarn(c.Request.Context(), fmt.Sprintf("微信支付 订单状态非法 trade_no=%s client_ip=%s", transaction.OutTradeNo, c.ClientIP()))
		default:
			logger.LogError(c.Request.Context(), fmt.Sprintf("微信支付 充值处理失败 trade_no=%s client_ip=%s error=%q", transaction.OutTradeNo, c.ClientIP(), err.Error()))
		}
		c.JSON(http.StatusOK, gin.H{"code": "FAIL", "message": "充值处理失败"})
		return
	}

	if alreadyDone {
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("微信支付 重复回调幂等忽略 trade_no=%s client_ip=%s", transaction.OutTradeNo, c.ClientIP()))
	} else {
		logger.LogInfo(c.Request.Context(), fmt.Sprintf("微信支付 充值成功 trade_no=%s client_ip=%s", transaction.OutTradeNo, c.ClientIP()))
	}
	c.JSON(http.StatusOK, gin.H{"code": "SUCCESS", "message": "成功"})
}
