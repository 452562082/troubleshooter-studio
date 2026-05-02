// dsTypes.ts —— Step 7 数据层 UI 类型,InitPage + DataStoreServiceBlock 共享。

export interface DSScanState {
  status: 'ok' | 'empty' | 'skipped' | 'error'
  reason?: string
}

export interface DSProbeState {
  status: 'idle' | 'loading' | 'ok' | 'fail'
  latency?: string
  detail?: string
  error?: string
}
