# CI/CD 自动 Release 配置

`.gitlab-ci.yml` 里的 `release:patch` / `release:minor` / `release:major` 三个 manual job
跟本地 `make bump-{patch,minor,major}` 完全等价 —— 区别是跑在 CI(macos runner),
触发方式从命令行换成 GitLab Pipeline 页面的按钮点击。

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

GitLab shared runner 默认没 macOS 节点,公司内部不要的话只能本地 `make bump-*`。

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
runner 跑 make bump-X:
   - 算版本号(从 git describe)
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

## 跟本地 `make bump-*` 的关系

CI release jobs **不取代**本地 `make bump-*`,只是另一种触发方式:

| 场景 | 推荐路径 |
|---|---|
| 开发机有完整环境 + 想自己看 changelog 再发 | 本地 `make bump-minor` |
| 远程开发 / 想团队 review pipeline / 多人协作 | CI manual 按钮 |
| Hotfix 着急发 | 都行,看哪个手边方便 |

两边底层都是 `tag-and-release` → `release-publish`,产物字节级一致(只要 git commit 一致)。
