/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { useEffect, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { QRCodeSVG } from 'qrcode.react'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { getUserBillingHistory } from '../../api'

interface WechatQrCodeDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  codeUrl: string
  tradeNo?: string
  onPaid?: () => void
}

export function WechatQrCodeDialog({
  open,
  onOpenChange,
  codeUrl,
  tradeNo,
  onPaid,
}: WechatQrCodeDialogProps) {
  const { t } = useTranslation()
  const onPaidRef = useRef(onPaid)
  onPaidRef.current = onPaid

  useEffect(() => {
    if (!open || !tradeNo) return

    const timer = window.setInterval(async () => {
      try {
        const res = await getUserBillingHistory(1, 10)
        const items = res?.data?.items ?? []
        const order = items.find((item) => item.trade_no === tradeNo)
        if (order?.status === 'success') {
          window.clearInterval(timer)
          toast.success(t('支付成功'))
          onPaidRef.current?.()
          onOpenChange(false)
        } else if (order?.status === 'expired') {
          window.clearInterval(timer)
          toast.error(t('订单已过期，请重新发起支付'))
        }
      } catch {
        // 网络抖动忽略，继续轮询
      }
    }, 3000)

    return () => window.clearInterval(timer)
  }, [open, tradeNo, onOpenChange, t])

  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title={t('微信扫码支付')}
      contentClassName='sm:max-w-sm'
    >
      <div className='flex flex-col items-center gap-4 py-4'>
        <QRCodeSVG value={codeUrl} size={220} />
        <p className='text-muted-foreground text-center text-sm'>
          {t('请使用微信扫描二维码完成支付，支付成功后自动到账')}
        </p>
        {tradeNo ? (
          <p className='text-muted-foreground text-xs'>{tradeNo}</p>
        ) : null}
      </div>
    </Dialog>
  )
}