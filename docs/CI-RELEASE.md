# CI/CD 自动 Release 配置

`.gitlab-ci.yml` 里的 `release:patch` / `release:minor` / `release:major` 三个 job
是**唯一**发版本入口 —— 本地 `make bump-*` / `tag-and-release` 已删(强制 release 走 CI,
版本号决策有 audit trail)。底层调 `scripts/release.sh patch|minor|major`,
带 `$CI` 环境变量校验防本地误调。

**典型流程**(用户视角):
1. 在 test(或 feature)分支干活
2. 提 MR 合到 main(MR pipeline 自动跑 lint/test 反馈)
3. 合并到 main:
   - **默认 → 自动 patch**(vX.Y.Z → vX.Y.(Z+1)),无需任何操作
   - 想发 minor → MR 标题或 squash msg 写 `[release:minor]` → 自动 minor(patch 跳过)
   - 想发 major → MR 标题或 squash msg 写 `[release:major]` → 自动 major
4. 几分钟后 GitLab Release 页面就有新版本(dmg + 跨平台 CLI binary,描述 = 自动 changelog)
5. 一次合并永远只产出 1 个 release

**应急/兜底**:三个 release job 在 Pipeline 页面还能看到对应 manual ▶ 按钮(只 main 上出现),用于
runner 故障重跑、首次没发出来手动补、或者临时想从 patch 切到 minor 但 commit msg 没写 marker
的场景 —— 直接点对应按钮即可。

---

## 一次性配置(管理员做)

### 1. 创建 Project Access Token

`Settings → Access Tokens` → 新建 token:

| 字段 | 值 |
|---|---|
| Name | `ci-release-bot` |
| Role | **Maintainer**(需要 push tag 过 protected v* pattern + 调 Release API) |
| Scopes | ✅ `api` ✅ `write_repository` |
| Expiration | 1 年(到期前提前续) |

**复制 token 字符串,只显示一次。** 形如 `glpat-xxxxxxxxxxxxxxxxxxxx`。

### 2. 注入 CI/CD Variable

`Settings → CI/CD → Variables` → Add Variable:

| 字段 | 值 |
|---|---|
| Key | `GITLAB_TOKEN` |
| Value | 上一步复制的 `glpat-...` |
| Type | Variable |
| Environment scope | `*` |
| Visibility | **Masked** ✅(防日志泄漏) |
| Flags | **Protect variable** ✅(只在 protected branch / tag job 暴露) |

### 3. 保护 v* tag pattern

`Settings → Repository → Protected tags` → 加规则:

| 字段 | 值 |
|---|---|
| Tag | `v*` |
| Allowed to create | **Maintainers**(对应步骤 1 的 token role) |

不保护的话 token 推 tag 仍能成功,但任何 developer 都能 push tag,无版本号管控。

### 4. macOS runner 上线

至少一台 gitlab-runner 注册到 Mac mini / MacBook 等 macOS 物理机或 VM,**两个配置必须同时满足**:

1. **Tags 字段填 `macos`**(再加什么 tag 都行,逗号分隔)
   - `.gitlab-ci.yml` 里 desktop / release:* 都用 `tags: [macos]`,匹配不上就 job 一直 pending
   - 注册时如果只填 `runner`、`mac-mini` 之类,得在 GitLab UI 后台 **Settings → CI/CD → Runners → ✏ 编辑** 加上 `macos`
2. **"Run untagged jobs" 必须勾上**
   - `go:lint` / `go:test` / `web` 这三个 test stage 的 job **没有 tags**,GitLab Runner 默认只接 tag 匹配的 job
   - 不勾这条,就算 runner 在线也只能接 desktop/release(tag 匹配的),test stage 永远 pending

> **踩过的坑**:第一次部署 Mac mini runner,只填了 `runner` tag,没勾 untagged。结果 pipeline 三个 test job 永远 pending,GitLab UI 显示 "job is stuck because the project doesn't have any runners online assigned to it"(其实 runner 在线,只是 tag 不匹配 + 不接 untagged)。

预装依赖:

```bash
xcode-select --install            # Xcode CLI Tools(cgo 链 UniformTypeIdentifiers.framework)
brew install go node jq           # Go 1.25+ / Node 20+ / jq(GitLab API JSON 解析)
```

**runner shell 也建议永久配国内 Go 镜像**(`.gitlab-ci.yml` 已全局注入 `GOPROXY` / `GOSUMDB`,但 runner 上跑 `make` / 调试 / pre-build 时仍会用 shell 环境):

```bash
# 写进 ~/.zshrc 或 /etc/environment(runner 用户)
go env -w GOPROXY=https://goproxy.cn,https://goproxy.io,direct
go env -w GOSUMDB=sum.golang.google.cn
```

> **踩过的坑**:Mac mini runner 第一次跑 `go:test` 全部 i/o timeout —— `proxy.golang.org` 是 Google 域,国内 IDC 不可达。`.gitlab-ci.yml` `variables:` 块加了 `GOPROXY` / `GOSUMDB` 后修复。如果以后增加新 Go job 或 runner 上手动跑 `go mod download`,记得这两个环境变量必须有。

GitLab shared runner 默认没 macOS 节点,公司内部不准备的话**完全发不了版本**(本地一键发布已禁)。
紧急 hotfix 场景退路:`make release-tag VERSION=v0.x.x` + 手动 `git push --tags`,binary 漏装就漏装。

---

## 用户视角的 release 流程

```
test/feature 分支 commit → push → 提 MR(MR pipeline 跑 lint/test)
   ↓
MR merge to main
   ↓
GitLab Pipeline 自动跑:
   - go test / web build / desktop build(后者 manual,不阻塞)
   - release:patch 自动触发(默认);如果 commit msg 含 [release:minor] / [release:major],
     则跳过 patch,跑对应 minor/major job
   - manual 按钮仍保留(应急用)
   ↓
runner 跑 scripts/release.sh X:
   - $CI 环境校验(本地误调拒)
   - 算版本号(从 git describe + LEVEL 递增)
   - 工作树/tag 重名校验
   - scripts/changelog.sh 生成 release notes
   - git tag -a 写 annotation
   - git push origin <branch> + git push origin <tag>
   - make release-publish:多平台编译 + 打 dmg + GitLab API 上传 Release
   ↓
Release 页面看到新版本,描述 = 干净 changelog,assets 齐全
```

**整个流程零本地构建。** 默认每次 main 合并自动 release(patch),团队任何人合 MR 都触发,
无需点按钮、无需本地操作 —— 真正的"合并即发版"。

---

## Failure modes

| 现象 | 原因 | 修 |
|---|---|---|
| `✗ CI/CD Variables 缺 GITLAB_TOKEN` | 步骤 2 没做 | 加 var |
| `git push: 401 Unauthorized` | token role 不是 maintainer / protected tags 没配 maintainer | 改步骤 1 + 3 |
| `command not found: brew` | runner 不是 macOS / Xcode CLI 没装 | 看步骤 4 |
| `go: ... dial tcp ... i/o timeout`(proxy.golang.org / sum.golang.org) | `proxy.golang.org` / `sum.golang.org` 是 Google 域,国内 runner 不可达 | `.gitlab-ci.yml` 已注入 `GOPROXY=https://goproxy.cn,https://goproxy.io,direct` + `GOSUMDB=sum.golang.google.cn`;runner shell 也 `go env -w` 配一份 |
| `dist/bin/tshoot-darwin-arm64: file not found` | runner 没 cross-compile env | 检查 Makefile release target,看 `GOOS=... GOARCH=... go build` 是否需要额外配置 |
| pipeline 上没看到 release:* 按钮 | 不在 main 分支 | 这 3 job 只 main 上出现(`rules: $CI_COMMIT_BRANCH == "main"`) |
| **job 一直 pending,GitLab 显示 "no runners online"**(但 runner 列表里明明 Online) | runner tag 跟 .gitlab-ci.yml `tags:` 对不上 / runner 没勾 "Run untagged jobs" | Settings → CI/CD → Runners → ✏ 编辑 runner:Tags 加 `macos` + 勾 ☑ Run untagged jobs。详细见步骤 4。|

---

## 本地不允许发布(已禁)

历史上有 `make bump-{patch,minor,major}` 本地一键发布,**已删** —— 强制所有 release 走 CI 有几个真好处:

- **单一来源**:版本号决策有 audit trail(谁点的 / 什么时间 / 哪个 commit),git history 干净
- **统一身份**:所有 tag 都是 CI 用同一身份打,Tagger 字段一致
- **强制 review**:发版前必跑全套 CI(go test / lint / vue-tsc / vite build / desktop check 等)
- **多人协作**:不会两个人同时本地 bump 撞 tag

本地仅保留**预览**入口(不动 git):
| 命令 | 作用 |
|---|---|
| `make release-notes` | 看下次发版会产生什么 changelog(scripts/changelog.sh 干跑) |
| `scripts/release.sh patch --print-only` | 看版本号会算成几(`v0.9.0` → `v0.9.1`) |
| `make release-tag VERSION=v0.x.x` | 紧急/迁移场景:本地打个 tag 不 push 不 publish。**正常发版别用** |
| `make release-publish VERSION=v0.x.x` | 对已有 tag 重传 binary(运维场景:之前 release 漏传 / 重新构建) |

`scripts/release.sh patch/minor/major`(不带 `--print-only`)在本地直接调会被拒,看到 `❌ 仅供 GitLab CI 调用`。
