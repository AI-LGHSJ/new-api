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
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

import { Dialog } from '@/components/dialog'
import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { fetchTokenKey, getApiKeys } from '@/features/keys/api'
import { API_KEY_STATUS } from '@/features/keys/constants'
import { requireServerSuccess } from '@/lib/server-error-message'

const INFINITE_CANVAS_URL = 'https://www.lghsj.cn'

function getServerAddress(): string {
  try {
    const raw = localStorage.getItem('status')
    if (raw) {
      const status = JSON.parse(raw) as { server_address?: string }
      if (status.server_address) return status.server_address
    }
  } catch {
    /* empty */
  }
  return window.location.origin
}

interface Props {
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function InfiniteCanvasDialog(props: Props) {
  const { t } = useTranslation()
  const [selectedKeyId, setSelectedKeyId] = useState<string>('')
  const [loading, setLoading] = useState(false)

  const { data } = useQuery({
    queryKey: ['infinite-canvas-keys'],
    queryFn: async () =>
      requireServerSuccess(await getApiKeys({ p: 1, size: 50 })),
    enabled: props.open,
    staleTime: 60 * 1000,
  })

  const enabledKeys = (data?.data?.items ?? []).filter(
    (item) => item.status === API_KEY_STATUS.ENABLED
  )

  const keyItems = enabledKeys.map((item) => ({
    value: String(item.id),
    label: item.name || `Key #${item.id}`,
  }))

  const selectedKey = enabledKeys.find(
    (item) => String(item.id) === selectedKeyId
  )

  const handleOpen = async () => {
    if (!selectedKeyId) {
      toast.warning(t('Please select an API key'))
      return
    }
    setLoading(true)
    try {
      const result = await fetchTokenKey(Number(selectedKeyId))
      if (!result.success || !result.data?.key) {
        toast.error(t('Failed to load API key'))
        return
      }
      const apiKey = `sk-${result.data.key}`
      const baseUrl = getServerAddress()
      const url = `${INFINITE_CANVAS_URL}/?baseUrl=${encodeURIComponent(baseUrl)}&apiKey=${encodeURIComponent(apiKey)}`
      window.open(url, '_blank', 'noopener')
      props.onOpenChange(false)
    } finally {
      setLoading(false)
    }
  }

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('导入到无限画布')}
      description={t(
        '选择 API Key 后将在新标签页打开无限画布，自动填入网关地址并拉取可用模型'
      )}
      contentClassName='sm:max-w-md'
      footer={
        <>
          <Button variant='outline' onClick={() => props.onOpenChange(false)}>
            {t('Cancel')}
          </Button>
          <Button onClick={handleOpen} disabled={loading}>
            {t('打开无限画布')}
          </Button>
        </>
      }
    >
      <div className='space-y-4'>
        <div className='space-y-2'>
          <Label>{t('选择 API 密钥')}</Label>
          <Select
            items={keyItems}
            value={selectedKeyId}
            onValueChange={(value) => setSelectedKeyId(value ?? '')}
          >
            <SelectTrigger className='w-full'>
              <SelectValue placeholder={t('选择一个已启用的密钥')} />
            </SelectTrigger>
            <SelectContent align='start' alignItemWithTrigger={false}>
              {enabledKeys.length === 0 ? (
                <div className='text-muted-foreground px-2 py-6 text-center text-sm'>
                  {t('暂无已启用的密钥')}
                </div>
              ) : (
                enabledKeys.map((item) => (
                  <SelectItem key={item.id} value={String(item.id)}>
                    {item.name || `Key #${item.id}`}
                  </SelectItem>
                ))
              )}
            </SelectContent>
          </Select>
          {selectedKey?.group ? (
            <p className='text-muted-foreground text-sm'>
              {t('分组')}：{selectedKey.group}
            </p>
          ) : null}
        </div>
      </div>
    </Dialog>
  )
}
