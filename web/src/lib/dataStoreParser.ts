// dataStoreParser —— 纯函数:从配置中心 / kuboard ConfigMap 拉到的原文里启发式识别数据层组件配置块。
//
// 暴露:
//   - DSMatcher / DS_MATCHERS         redis/mongodb/mysql/pg/es/kafka/rocketmq/rabbitmq/ck 启发式
//   - parseConfigContent(text, fmt)   yaml / json / properties / k8s-env-flat / yaml-multi 五种格式分发
//   - findKey / pickConnection / str / extractPort   matcher 内部用的小工具
//
// 不依赖 Vue,纯输入输出;useDataStoreScan / autoImportDataStores 调用方持有 DS_MATCHERS 跑识别。
import yaml from 'js-yaml'

export interface DSMatcher {
  dsKey: string // 对齐 DS_TOOL_SPECS 里 spec.key
  /** matchYAML 接受解析后的 js-yaml 对象(object 根),返回识别到的字段 map(若该 ds 未配置则返 null) */
  matchYAML: (root: any) => Record<string, string> | null
}

// ── 小工具 ──

// 深度在第 1 / 2 / 3 层找 key(配置 yaml 常见结构如 `spring.redis` 或 `databases.redis`)
export function findKey(obj: any, keys: string[], depth: number = 3): any {
  if (!obj || typeof obj !== 'object') return null
  for (const k of keys) {
    if (obj[k] !== undefined) return obj[k]
  }
  if (depth <= 1) return null
  for (const v of Object.values(obj)) {
    const r = findKey(v, keys, depth - 1)
    if (r) return r
  }
  return null
}

// pickConnection 处理"组件根下可能还嵌一层 connection 名"的情况。
// 例 redis 根 = { default: { host, port }, cache: {...} },我们挑第一个包含目标字段的 child;
// 如果组件根自己就有目标字段(如 redis.host 平铺),直接返回根。
export function pickConnection(block: any, targetFields: string[]): any {
  if (!block || typeof block !== 'object' || Array.isArray(block)) return null
  for (const f of targetFields) {
    if (block[f] !== undefined && block[f] !== null) return block
  }
  for (const v of Object.values(block)) {
    if (!v || typeof v !== 'object' || Array.isArray(v)) continue
    for (const f of targetFields) {
      if ((v as any)[f] !== undefined && (v as any)[f] !== null) return v
    }
  }
  return null
}

export function str(v: any): string {
  if (v === undefined || v === null) return ''
  return String(v).trim()
}

export function extractPort(addr: string): string {
  const m = addr.match(/:(\d+)$/)
  return m ? m[1] : ''
}

// ── 启发式 matchers ──
// 针对常见 Go / Java / Hyperf / Spring 应用配置的结构,启发式识别。多份 yaml 会合并(取第一条匹上的)。
// 每个 field key 对齐 DS_TOOL_SPECS 里 spec.fields[i].key。
//
// 关键点:yaml 里很多组件有"connection 名"这一层(如 `redis.default.host` / `databases.primary.host`),
// 所以 matcher 拿到"组件根"后要再 pickConnection(block, [host, url...]) —— 这个 helper 支持
// block 直接带 host 字段,或 block 下某个 child 对象带 host 字段。
export const DS_MATCHERS: DSMatcher[] = [
  {
    dsKey: 'redis',
    matchYAML: (r) => {
      const block = findKey(r, ['redis', 'Redis', 'REDIS'])
      const c = pickConnection(block, ['host', 'url', 'address', 'uri'])
      if (!c) return null
      const host = str(c.host) || str(c.address)
      const port = str(c.port) || extractPort(str(c.address))
      const pass = str(c.password) || str(c.auth)
      const db = str(c.db) || str(c.database)
      const explicit = str(c.url) || str(c.uri)
      if (explicit) return { url: explicit }
      if (!host) return null
      return { url: `redis://${pass ? ':' + pass + '@' : ''}${host}${port ? ':' + port : ''}${db ? '/' + db : ''}` }
    },
  },
  {
    dsKey: 'mongodb',
    matchYAML: (r) => {
      const block = findKey(r, ['mongodb', 'mongo', 'MongoDB'])
      const c = pickConnection(block, ['uri', 'url', 'dsn', 'host'])
      if (!c) return null
      const uri = str(c.uri) || str(c.url) || str(c.dsn)
      if (uri) return { uri }
      const host = str(c.host), port = str(c.port), user = str(c.user) || str(c.username), pass = str(c.password), database = str(c.database) || str(c.db)
      if (!host) return null
      return { uri: `mongodb://${user ? user + (pass ? ':' + pass : '') + '@' : ''}${host}${port ? ':' + port : ''}${database ? '/' + database : ''}` }
    },
  },
  {
    dsKey: 'mysql',
    matchYAML: (r) => {
      // 三类常见布局:mysql.default.* / databases.primary(driver=mysql) / datasource/db
      let c: any = null
      const mysqlBlock = findKey(r, ['mysql', 'MySQL'])
      c = pickConnection(mysqlBlock, ['host', 'dsn', 'url', 'uri'])
      if (!c) {
        const dbBlock = findKey(r, ['databases', 'datasource', 'database', 'db'])
        if (dbBlock && typeof dbBlock === 'object') {
          for (const v of Object.values(dbBlock)) {
            if (!v || typeof v !== 'object') continue
            const driver = str((v as any).driver || (v as any).dialect).toLowerCase()
            if (driver === 'mysql' || driver.includes('mysql')) { c = v; break }
            if (!driver && (str((v as any).host) || str((v as any).dsn))) { c = v; break }
          }
          if (!c) c = pickConnection(dbBlock, ['host', 'dsn', 'url'])
        }
      }
      if (!c) return null
      const dsn = str(c.dsn) || str(c.uri) || str(c.url)
      if (dsn && /mysql|tcp\(.*\)/i.test(dsn)) return { dsn }
      const host = str(c.host), port = str(c.port), user = str(c.user) || str(c.username), pass = str(c.password), database = str(c.database) || str(c.name)
      if (!host) return null
      return { dsn: `${user || ''}${pass ? ':' + pass : ''}@tcp(${host}${port ? ':' + port : '3306'})/${database || ''}` }
    },
  },
  {
    dsKey: 'postgresql',
    matchYAML: (r) => {
      let c: any = null
      const pgBlock = findKey(r, ['postgres', 'postgresql', 'pg'])
      c = pickConnection(pgBlock, ['host', 'dsn', 'url', 'uri'])
      if (!c) {
        const dbBlock = findKey(r, ['databases', 'datasource', 'database'])
        if (dbBlock && typeof dbBlock === 'object') {
          for (const v of Object.values(dbBlock)) {
            if (!v || typeof v !== 'object') continue
            const driver = str((v as any).driver || (v as any).dialect).toLowerCase()
            if (driver === 'postgres' || driver === 'postgresql' || driver === 'pg') { c = v; break }
          }
        }
      }
      if (!c) return null
      const dsn = str(c.dsn) || str(c.uri) || str(c.url)
      if (dsn) return { dsn }
      const host = str(c.host), port = str(c.port), user = str(c.user) || str(c.username), pass = str(c.password), database = str(c.database) || str(c.name)
      if (!host) return null
      return { dsn: `postgres://${user || ''}${pass ? ':' + pass : ''}@${host}${port ? ':' + port : ''}/${database || ''}` }
    },
  },
  {
    dsKey: 'elasticsearch',
    matchYAML: (r) => {
      const block = findKey(r, ['elasticsearch', 'es'])
      if (!block || typeof block !== 'object') return null
      const c = pickConnection(block, ['url', 'endpoint', 'hosts', 'host'])
      if (!c) return null
      const url = str(c.url) || str(c.endpoint) || (Array.isArray(c.hosts) && c.hosts[0]) || str(c.host)
      if (!url) return null
      return {
        url,
        user: str(c.username) || str(c.user) || '',
        pass: str(c.password) || '',
      }
    },
  },
  {
    dsKey: 'kafka',
    matchYAML: (r) => {
      const block = findKey(r, ['kafka'])
      if (!block || typeof block !== 'object') return null
      const c = pickConnection(block, ['brokers', 'servers', 'bootstrap_servers', 'bootstrapServers'])
      if (!c) return null
      const brokers = (Array.isArray(c.brokers) && c.brokers.join(',')) || str(c.brokers) ||
                      (Array.isArray(c.servers) && c.servers.join(',')) || str(c.servers) ||
                      str(c.bootstrap_servers) || str(c.bootstrapServers)
      if (!brokers) return null
      return {
        brokers,
        user: str(c.username) || str(c.sasl_username) || '',
        pass: str(c.password) || str(c.sasl_password) || '',
      }
    },
  },
  {
    dsKey: 'rocketmq',
    matchYAML: (r) => {
      const block = findKey(r, ['rocketmq', 'rocket_mq', 'rocketMQ'])
      const c = pickConnection(block, ['namesrv', 'name_srv', 'nameserver', 'nameServer', 'servers', 'host'])
      if (!c) return null
      const namesrv = str(c.namesrv) || str(c.name_srv) || str(c.nameserver) || str(c.nameServer) || str(c.servers) ||
                      (str(c.host) ? `${c.host}${c.port ? ':' + c.port : ''}` : '')
      if (!namesrv) return null
      return { namesrv }
    },
  },
  {
    dsKey: 'rabbitmq',
    matchYAML: (r) => {
      const block = findKey(r, ['rabbitmq', 'amqp'])
      const c = pickConnection(block, ['url', 'uri', 'dsn', 'host'])
      if (!c) return null
      const url = str(c.url) || str(c.uri) || str(c.dsn)
      if (url) return { url }
      const host = str(c.host), port = str(c.port), user = str(c.user) || str(c.username), pass = str(c.password), vhost = str(c.vhost)
      if (!host) return null
      return { url: `amqp://${user || ''}${pass ? ':' + pass : ''}@${host}${port ? ':' + port : ''}${vhost ? '/' + vhost : ''}` }
    },
  },
  {
    dsKey: 'clickhouse',
    matchYAML: (r) => {
      const block = findKey(r, ['clickhouse', 'ck', 'ClickHouse'])
      const c = pickConnection(block, ['url', 'host', 'addr'])
      if (!c) return null
      const url = str(c.url) || str(c.addr) || str(c.host)
      if (!url) return null
      return {
        url,
        user: str(c.user) || str(c.username) || '',
        pass: str(c.password) || '',
      }
    },
  },
]

// ── 内容解析器 ──

// 把后端返回的原文按 format 解析成 object;yaml/properties/json 都支持
// yaml-multi:Kuboard configmap 把多个 data 字段拼成 multi-doc YAML(--- 分隔),
// 这里 loadAll 拿到 N 个根对象后浅合并成一个,DS_MATCHERS 直接吃。
export function parseConfigContent(content: string, format?: string): any {
  const fmt = (format || '').toLowerCase()
  try {
    if (fmt === 'json') return JSON.parse(content)
    if (fmt === 'properties') return parseProperties(content)
    if (fmt === 'k8s-env-flat') {
      // K8s ConfigMap 的 data 是平铺 KV(典型 Laravel/Spring .env 用法,字段名即 env 变量名)。
      // 把扁平 KEY=VALUE 重塑成 {redis:{host,port,password,...}, mysql:{...}, ...} 让现有
      // DS_MATCHERS(用 findKey + pickConnection)能找到。原 flat key 仍保留以备其他规则查找。
      let flat: Record<string, string> = {}
      try { flat = JSON.parse(content) } catch { flat = {} }
      return envFlatToRoot(flat)
    }
    if (fmt === 'yaml-multi') {
      // ConfigMap 各 data 字段拼成的多 doc:每段可能是 yaml / json / properties / 其他。
      // yaml.load 对 properties 形会得到 scalar 字符串;但反过来 parseProperties 对 URL /
      // base64 / 证书 等任意文本也会强行按 ":" / "=" 切出假 key(典型坑:base64 / https 顶级 key)。
      // 所以要严格判断:只在内容明显是 properties(有合理比例的 IDENTIFIER=VALUE 或
      // IDENTIFIER:VALUE 行)时才走 properties 兜底。
      const merged: Record<string, any> = {}
      const segments = content.split(/^---\s*$/m)
      for (const seg of segments) {
        const text = seg.trim()
        if (!text) continue
        let parsed: any = null
        try { parsed = yaml.load(text) } catch { parsed = null }
        if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
          if (looksLikeProperties(text)) {
            try { parsed = parseProperties(text) } catch { parsed = null }
          }
        }
        if (parsed && typeof parsed === 'object' && !Array.isArray(parsed)) {
          Object.assign(merged, parsed)
        }
      }
      return merged
    }
    // 默认按 yaml 试 —— js-yaml 兼容大部分 scalar 单值的 properties 也能勉强吃
    return yaml.load(content)
  } catch {
    // 最后降级:按 properties 试一次
    try { return parseProperties(content) } catch { return null }
  }
}

// envFlatToRoot:把 K8s ConfigMap 的扁平 .env 形态(DB_HOST=... / REDIS_PORT=...)
// 重塑成 DS_MATCHERS 能直接匹的嵌套对象 {redis:{...},mysql:{...},mongodb:{...},...}。
// 原始 flat key 仍保留在 root 里,以备未来扩展规则。
//
// 前缀映射(大小写不敏感):
//   REDIS_*                       → redis
//   MONGO_* / MONGODB_*           → mongodb
//   ES_* / ELASTIC_* / ELASTICSEARCH_* → elasticsearch
//   KAFKA_*                       → kafka
//   MYSQL_*                       → mysql
//   PGSQL_* / POSTGRES_* / POSTGRESQL_* → pgsql
//   DB_*  → 由 DB_CONNECTION 决定(mysql / pgsql / sqlite / etc)
//
// 字段名归一化(小写):
//   HOST/HOSTS/SERVER → host  PORT → port  USERNAME/USER → username
//   PASSWORD/PASS/PWD/AUTH → password   DATABASE/DB/DBNAME/NAME → database
//   URI → uri   URL → url   DSN → dsn
function envFlatToRoot(flat: Record<string, string>): Record<string, any> {
  const root: Record<string, any> = { ...flat }

  const normField = (s: string): string => {
    const k = s.toLowerCase()
    if (k === 'host' || k === 'hosts' || k === 'server' || k === 'addr' || k === 'address') return 'host'
    if (k === 'port') return 'port'
    if (k === 'username' || k === 'user') return 'username'
    if (k === 'password' || k === 'pass' || k === 'pwd' || k === 'auth') return 'password'
    if (k === 'database' || k === 'db' || k === 'dbname' || k === 'name') return 'database'
    if (k === 'uri') return 'uri'
    if (k === 'url') return 'url'
    if (k === 'dsn') return 'dsn'
    if (k === 'brokers' || k === 'bootstrap_servers' || k === 'bootstrap') return 'brokers'
    if (k === 'index') return 'index'
    if (k === 'sasl_username' || k === 'sasl_user') return 'sasl_username'
    if (k === 'sasl_password' || k === 'sasl_pass') return 'sasl_password'
    return k
  }

  const groupBy = (prefixes: string[], component: string) => {
    const block: Record<string, any> = root[component] && typeof root[component] === 'object' && !Array.isArray(root[component]) ? root[component] : {}
    let touched = false
    for (const [k, v] of Object.entries(flat)) {
      if (typeof v !== 'string') continue
      for (const p of prefixes) {
        if (k.toUpperCase().startsWith(p + '_')) {
          const tail = k.substring(p.length + 1)
          const nf = normField(tail)
          if (nf === 'sasl_username' || nf === 'sasl_password') {
            if (!block.sasl || typeof block.sasl !== 'object') block.sasl = {}
            block.sasl[nf === 'sasl_username' ? 'username' : 'password'] = v
          } else {
            block[nf] = v
          }
          touched = true
          break
        }
      }
    }
    if (touched) root[component] = block
  }

  groupBy(['REDIS'], 'redis')
  groupBy(['MONGO', 'MONGODB'], 'mongodb')
  groupBy(['ES', 'ELASTIC', 'ELASTICSEARCH'], 'elasticsearch')
  groupBy(['KAFKA'], 'kafka')
  groupBy(['MYSQL'], 'mysql')
  groupBy(['PGSQL', 'POSTGRES', 'POSTGRESQL'], 'pgsql')

  // DB_* 归到 DB_CONNECTION 指明的 driver 下(Laravel 风格)
  const dbConn = (flat['DB_CONNECTION'] || flat['db_connection'] || '').toLowerCase()
  if (dbConn) {
    const dbDriver =
      dbConn === 'mysql' ? 'mysql' :
      (dbConn === 'pgsql' || dbConn === 'postgres' || dbConn === 'postgresql') ? 'pgsql' :
      (dbConn === 'mongodb' || dbConn === 'mongo') ? 'mongodb' :
      ''
    if (dbDriver) {
      const block: Record<string, any> = (root[dbDriver] && typeof root[dbDriver] === 'object' && !Array.isArray(root[dbDriver])) ? root[dbDriver] : {}
      for (const [k, v] of Object.entries(flat)) {
        if (typeof v !== 'string') continue
        if (!k.toUpperCase().startsWith('DB_')) continue
        const tail = k.substring(3)
        if (tail.toUpperCase() === 'CONNECTION') continue
        block[normField(tail)] = v
      }
      root[dbDriver] = block
    }
  }

  return root
}

// 严格判断"这段文本是 properties 风格"。规则:
//   - 排除明显的 URL / data: URI / 证书块开头(避免被强切 key);
//   - 至少 2 条 IDENTIFIER=VALUE 或 IDENTIFIER:VALUE 行,且占非空行 50% 以上;
//   - IDENTIFIER 必须是合法标识符(可含 . _ -),否则视为伪命中。
function looksLikeProperties(text: string): boolean {
  const lines = text.split(/\r?\n/).map(l => l.trim()).filter(l => l && !l.startsWith('#') && !l.startsWith('!'))
  if (lines.length === 0) return false
  const head = lines[0]
  if (/^(https?|ftp|wss?):\/\//i.test(head)) return false
  if (/^data:[a-z]+\//i.test(head)) return false
  if (head.startsWith('-----BEGIN ')) return false
  if (head.startsWith('<')) return false // html/xml
  const propRe = /^[a-zA-Z_][\w.\-]*\s*[=:]\s*\S/
  let propCount = 0
  for (const l of lines) {
    if (propRe.test(l)) propCount++
  }
  return propCount >= 2 && propCount / lines.length >= 0.5
}

// 极简 properties 解析:`k.v.x = y` → 嵌套对象 {k: {v: {x: "y"}}}
function parseProperties(text: string): Record<string, any> {
  const out: Record<string, any> = {}
  for (const rawLine of text.split(/\r?\n/)) {
    const line = rawLine.trim()
    if (!line || line.startsWith('#') || line.startsWith('!')) continue
    const m = line.match(/^([^=:]+)[=:]\s*(.*)$/)
    if (!m) continue
    const key = m[1].trim(), val = m[2].trim()
    const segs = key.split('.')
    let cur: Record<string, any> = out
    for (let i = 0; i < segs.length - 1; i++) {
      const s = segs[i]
      if (typeof cur[s] !== 'object' || cur[s] === null) cur[s] = {}
      cur = cur[s]
    }
    cur[segs[segs.length - 1]] = val
  }
  return out
}
