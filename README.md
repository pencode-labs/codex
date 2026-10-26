# Codex Agent

Pencode 的 Codex 扩展，通过 `codex app-server` 的 stdio JSON-RPC 协议工作。

- 状态栏底部的 **Codex** 按钮打开或关闭输入面板。
- 发送消息后，在编辑器区域创建独立会话标签页，标题取自首条消息。
- 支持流式回复、Markdown、命令输出、文件修改、停止任务和逐次审批。
- 输入框采用圆角卡片，左下角 **+** 打开 **新会话 / 历史会话**，右下角的模型按钮向上展开选择菜单，当前模型右侧显示勾选标记；运行时发送按钮切换为停止按钮。
- 模型菜单通过 `model/list` 读取真实可用模型，每次连接缓存一次，支持方向键导航、Esc 和点击外部关闭；加载失败可重试。
- **新会话** 切换到新草稿；标签页或 **历史会话** 可继续已有会话，各会话保留输入草稿。
- 历史按当前项目目录筛选，支持分页会话列表和更早的消息记录。
- 关闭标签页只关闭视图，任务继续运行，历史仍由 Codex 保存。

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

Windows 单独构建时使用 `go build -o bin/codex-agent.exe .`。如需从扩展管理器选择本目录安装，将 `resources/extension.json` 的 `entry` 改为 `bin/codex-agent.exe`。本扩展依赖本次加入的 `peb.EditorTab` 接口，需要同时更新 Pencode 主程序。

Codex 默认从 `PATH` 查找。可在工作区服务器启动前设置 `PENCODE_CODEX_PATH` 为原生 Codex 可执行文件的完整路径；不经过 shell，不接受命令字符串。远程工作区中的进程、认证和文件操作均在服务器运行。

选择“默认”时使用 Codex 配置；选择具体模型时传给 `thread/start` 和 `turn/start`。新建或恢复会话使用 `workspace-write` 沙箱和 `on-request` 审批策略；命令、文件修改、额外权限请求在对应会话内展示，由用户逐次确认。支持 `item/tool/requestUserInput` 的文本回答。未实现的服务端请求返回明确的 JSON-RPC 错误，不会默默挂起。

## 验证

```sh
go test ./...
go test -race ./...
```

无网络的协议测试使用子进程模拟 app-server，覆盖握手、流式事件、审批、停止与会话恢复。可选真实 CLI 冒烟测试执行握手、读取模型列表并创建临时会话，不提交聊天任务：设置 `PENCODE_CODEX_INTEGRATION=1` 后运行 `go test -run TestLiveAppServer -v`。

消息与命令输出按条保留最多 128 KiB 的显示文本，标签页展示最近约 1.2 MiB 的 HTML。完整会话仍由 Codex 持久化。当前未实现图片附件、MCP 表单或账户登录界面；登录请使用 CLI。扩展视图不会加载模型输出中的图片资源。

协议依据：[OpenAI Codex app-server 文档](https://developers.openai.com/codex/app-server)。
