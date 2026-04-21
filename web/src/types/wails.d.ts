// Wails v2 注入的全局桥接对象。只在桌面 app 宿主里可用；
// 浏览器里（`tshoot serve` 或 `vite dev`）访问 window.go 会是 undefined。
// 页面代码要先判空再调用，未检测到就走回退（fetch API 或空态提示）。

export interface DiscoveredBotMeta {
  schema_version: number
  tshoot_version: string
  system_id: string
  system_name: string
  target: string
  generated_at: string
  system_yaml: string
  user_edits?: Record<string, unknown>
}

export interface DiscoveredBot {
  meta: DiscoveredBotMeta
  path: string
  mod_time: string
  env_count: number
  repo_count: number
  skill_count: number
  targets?: string[]
}

export interface ValidateResult {
  valid: boolean
  system: string
  name: string
  envs: number
  repos: number
}

export interface ApplyResult {
  agent_path: string
  target: string
  files_written: number
  files_preserved?: string[]
  files_removed?: string[]
  tsf_json_updated: boolean
  needs_restart_hint?: string
}

declare global {
  interface Window {
    go?: {
      main: {
        App: {
          Version(): Promise<string>
          DiscoverBots(extraRoots: string[]): Promise<DiscoveredBot[]>
          Validate(yamlText: string): Promise<ValidateResult>
          Gen(yamlText: string, outputDir: string): Promise<Record<string, unknown>>
          ApplyBot(agentPath: string, newYamlText: string, dryRun: boolean): Promise<ApplyResult>
          SaveYAML(defaultFilename: string, yamlText: string): Promise<string>
        }
      }
    }
  }
}

export {}
