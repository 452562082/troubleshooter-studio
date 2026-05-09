# CI/CD 自动 Release 配置

`.gitlab-ci.yml` 里的 `release:patch` / `release:minor` / `release:major` 三个 manual job
是**唯一**发版本入口 —— 本地 `make bump-*` / `tag-and-release` 已删(强制 release 走 CI,
版本号决策有 audit trail)。底层调 `scripts/release.sh patch|minor|major`,
带 `$CI` 环境变量校验防本地误调。

**典型流程**(用户视角):
1. 在 feature 分支干活
2. 提 MR 合到 main
3. main pipeline 跑完 test/build,看到 release 三个 manual 按钮
4. 决定本次是 patch / minor / major,点对应按钮
5. 几分钟后 GitLab Release 页面就有新版本(dmg + 跨平台 CLI binary,描述 = 自动 changelog)

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

至少一台 gitlab-runner 注册时打 `macos` tag,且预装:

```bash
xcode-select --install            # Xcode CLI Tools(cgo 链 UniformTypeIdentifiers.framework)
brew install go node jq           # Go 1.25+ / Node 20+ / jq(GitLab API JSON 解析)
```

GitLab shared runner 默认没 macOS 节点,公司内部不准备的话**完全发不了版本**(本地一键发布已禁)。
紧急 hotfix 场景退路:`make release-tag VERSION=v0.x.x` + 手动 `git push --tags`,binary 漏装就漏装。

---

## 用户视角的 release 流程

```
feature 分支 commit → push → 提 MR
   ↓
MR merge to main
   ↓
GitLab Pipeline 自动跑:
   - go test / web build / desktop build(都过)
   - release:{patch,minor,major} 出现 ▶ 三个 manual 按钮
   ↓
打开 Pipeline → 点对应按钮
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

**整个流程零本地构建。** 团队任何人都能在 GitLab UI 上发版本(只要他对 main 有 push 权限)。

---

## Failure modes

| 现象 | 原因 | 修 |
|---|---|---|
| `✗ CI/CD Variables 缺 GITLAB_TOKEN` | 步骤 2 没做 | 加 var |
| `git push: 401 Unauthorized` | token role 不是 maintainer / protected tags 没配 maintainer | 改步骤 1 + 3 |
| `command not found: brew` | runner 不是 macOS / Xcode CLI 没装 | 看步骤 4 |
| `dist/bin/tshoot-darwin-arm64: file not found` | runner 没 cross-compile env | 检查 Makefile release target,看 `GOOS=... GOARCH=... go build` 是否需要额外配置 |
| pipeline 上没看到 release:* 按钮 | 不在 main 分支 | 这 3 job 只 main 上出现(`rules: $CI_COMMIT_BRANCH == "main"`) |

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
