# Codex Agent

Pencode 的 Codex 扩展，通过 `codex app-server` 的 stdio JSON-RPC 协议工作。

- 状态栏底部的 **ChatGPT 图标按钮**（悬停显示 Codex Agent）打开或切回编辑器区域的聊天标签页；通过标签页的关闭按钮关闭后，可再次点击图标打开。
- 聊天标签页分为左右两栏：左侧显示当前聊天记录，输入框固定在聊天记录下方；右侧显示历史会话列表。发送消息或恢复会话后，标签标题跟随当前聊天标题。
- 两栏分别滚动；流式回复跟随最新消息，向上翻阅时保留阅读位置，输入焦点、文本选区和历史列表滚动位置在更新时保留。
- 支持流式回复、Markdown、命令输出、文件修改、停止任务和逐次审批。
- 聊天记录中，用户消息以右侧气泡展示，Codex 回复平铺显示；连续的思考、命令、读取、搜索和文件修改合并为默认折叠的活动分组。展开分组后显示单条摘要，再点击摘要查看思考内容、命令输出或 diff；长列表独立滚动，运行和失败状态在分组折叠时仍可见，展开状态随流式更新保留。
- 输入框采用圆角卡片，两侧保留 8px 间距。左下角 **+** 打开文件选择窗口，可浏览目录或输入路径添加项目外文件；已添加的文件显示在输入框内，点击可移除。文件引用随本次消息发送，发送失败时保留；新会话和各会话的附件草稿独立，远程项目使用工作区服务器上的路径。
- **新会话** 以笔形图标按钮显示在右侧历史栏标题旁，悬停提示“新会话”。右下角的模型按钮向上展开选择菜单，当前模型右侧显示勾选标记；运行时发送按钮切换为停止按钮。
- 新会话、刷新、添加文件、发送和停止等纯图标按钮统一为 24×24px，图标为 14×14px。
- 输入框按 Enter 发送消息，Shift+Enter 换行；输入法选词时按 Enter 不会发送。
- 模型菜单通过 `model/list` 读取真实可用模型，每次连接缓存一次；选中模型后继续选择该模型支持的推理强度，按钮在模型名称右侧同一行显示强度。菜单支持方向键导航、Esc 和点击外部关闭；加载失败可重试。返回模型列表或中途关闭不会修改原选择。
- 输入框左下角的权限按钮向上展开三档菜单：**请批准**、**帮我批准**、**完全访问权限**，当前选项右侧显示勾选。完全访问使用橙色提示；**了解更多** 展开权限说明。
- 模型、推理强度和权限按项目保存在 `.pencode/codex.json`，选择完成后立即保存，重启扩展或切换项目、会话标签页时恢复。新项目默认使用 Codex 配置的模型和 **请批准** 权限；从下一次发送开始应用于新建、恢复和已有会话，不影响正在执行的任务。权限与模型菜单互斥，隐藏或关闭聊天标签页会关闭菜单。
- **新会话** 切换到新草稿；右侧历史列表可继续已有会话，各会话保留输入草稿。历史列表的 **刷新** 重新读取当前项目的会话，**更多会话** 加载下一页。
- 历史按当前项目目录筛选，切换 IDE 活动项目时自动读取对应项目的会话，项目草稿独立保留。列表包含 CLI、VS Code 和 app-server 创建的会话，分页和刷新按会话 ID 去重，支持加载更早的消息记录。
- 切换会话或关闭标签页只切换视图，任务继续运行，历史仍由 Codex 保存。

## 构建与运行

需要 Go 1.27+，以及工作区服务器上的 Codex CLI。先在服务器运行 `codex login`。不单独存储 API Key 或登录信息。

从 Pencode 仓库根目录执行：

```sh
make install-extensions
make run
```

安装命令会自动发现本目录，构建扩展并为 Windows 添加 `.exe` 后缀。

单独开发：

```sh
cd extensions/codex
go test ./...
go build -o bin/codex-agent .
```

Windows 单独构建时使用 `go build -o bin/codex-agent.exe .`。如需从扩展管理器选择本目录安装，将 `resources/extension.json` 的 `entry` 改为 `bin/codex-agent.exe`。本扩展依赖 `peb.EditorTab` 及编辑器内分栏和滚动支持，需要同时更新 Pencode 主程序。

在 IDE **设置 → Codex Agent → Codex 可执行文件路径** 中指定原生 Codex 可执行文件或其所在目录；填写目录时使用其中的 `codex.exe`（Windows）或 `codex`（macOS/Linux），目录中没有该程序时提示具体错误。支持 Windows 路径（如 `C:\Program Files\Codex\codex.exe` 或 `C:/Tools/codex.exe`）、macOS/Linux 路径（如 `/usr/local/bin/codex`）、用户目录（如 `~/.local/bin/codex`，Windows 也支持 `~\bin\codex.exe`）以及相对于项目目录的路径（如 `./tools/codex`）。路径包含空格时无需添加引号；只填写命令名时从 `PATH` 查找。

此设置优先于环境变量 `PENCODE_CODEX_PATH`；留空时先使用该环境变量，再从 `PATH` 查找 `codex`。修改在下次连接时生效，已有连接可通过重启扩展切换，不中断正在运行的任务。可执行文件不经过 shell，不接受命令字符串。远程工作区使用服务器的路径格式，进程、认证和文件操作均在服务器运行。

项目设置保存在工作区服务器的项目根目录下，例如 `.pencode/codex.json`：

```json
{
  "model": "",
  "permission": "ask"
}
```

`model` 为空表示使用 Codex 默认模型；可选的 `reasoningEffort` 保存推理强度，取值来自模型目录（例如 `low`、`medium`、`high`）。选择“默认”同时清除模型和强度覆盖；未声明强度选项的模型直接完成选择。`permission` 可为 `ask`（请批准）、`auto`（帮我批准）或 `full`（完全访问权限）。配置读取失败时使用默认值并提示错误；保存失败时保留原选择并提示错误。打开项目不会创建配置文件，首次完成模型或权限选择时才写入。

选择“默认”时使用 Codex 配置；选择具体模型时传给 `thread/start` 和 `turn/start`，推理强度通过每次 `turn/start` 的 `effort` 字段传入。未选择强度时使用已加载模型目录的默认强度。权限在 `thread/start`、`thread/resume` 和每次 `turn/start` 显式传入，需要支持 `approvalsReviewer` 的 Codex CLI：

| 权限 | 沙箱 | 审批策略 | 审批者 |
| --- | --- | --- | --- |
| 请批准 | `workspace-write` | `on-request` | `user` |
| 帮我批准 | `workspace-write` | `on-request` | `auto_review` |
| 完全访问权限 | `danger-full-access` | `never` | `user` |

前两档的任务使用禁止网络访问的工作区沙箱，外部文件和网络访问需经过审批。自动审查由 Codex app-server 执行，可能拒绝风险操作并产生额外用量。完全访问允许直接执行操作。远程项目的权限作用于工作区服务器。交互审批请求在对应会话内展示，支持 `item/tool/requestUserInput` 的文本回答。未实现的服务端请求返回明确的 JSON-RPC 错误，不会默默挂起。

## 验证

```sh
go test ./...
go test -race ./...
```

无网络的协议测试使用子进程模拟 app-server，覆盖握手、流式事件、审批、停止与会话恢复，以及启动和历史读取阻塞时关闭、重开标签页。可选真实 CLI 冒烟测试执行握手、读取模型列表并创建临时会话，不提交聊天任务：设置 `PENCODE_CODEX_INTEGRATION=1` 后运行 `go test -run TestLiveAppServer -v`。

消息与命令输出按条保留最多 128 KiB 的显示文本，标签页展示最近约 1.2 MiB 的 HTML。完整会话仍由 Codex 持久化。当前未实现图片附件、MCP 表单或账户登录界面；登录请使用 CLI。扩展视图不会加载模型输出中的图片资源。

协议依据：[OpenAI Codex app-server 文档](https://developers.openai.com/codex/app-server)。
