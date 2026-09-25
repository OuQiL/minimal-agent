## Why

需要交付一个**不依赖任何现成 agent 框架**的最小可用 Agent，以证明对 Agent Runtime 底层机制的掌握。langgraph / openhands 这类框架恰好把被考察的三个核心问题——循环控制、工具调用协议、会话与上下文管理——全部封装掉了，直接用它们等于绕过了考察点。因此必须从零实现，用真实 LLM API 驱动，并配套可验证的测试用例。

## What Changes

- 新建 Go 模块（Go 1.25.3），实现一个 CLI 形态的 Agent Runtime，核心循环、工具协议、会话管理、上下文管理全部自行实现
- 实现 Agent Loop：接收用户输入 → 由 LLM 决策直接回复还是调用工具 → 执行工具 → 依据结果判断继续循环还是返回。循环设最大轮次上限，超限时安全收敛
- 实现流式输出：模型响应以增量方式实时呈现，思维链与最终答复分流展示；流式下需先完成工具调用分片的拼装，再执行工具
- 实现多工具并发执行：一次决策中请求的多个工具并发执行，结果按模型给出的调用序号**稳定定序**后回填，保证上下文顺序确定、测试可复现
- 实现工具注册机制：每个工具声明**名称、描述、参数 JSON Schema**，由 LLM 基于 Schema 自主决策调用；首批内置四个工具 `calculator`（本地求值）、`search`（接入博查真实搜索 API）、`weather`（接入 Open-Meteo 真实天气 API）、`todo`（会话内持久待办）。注册表支持**名称别名**与**未知工具兜底**，以应对模型对工具名的幻觉
- 实现 LLM 输出解析逻辑：从模型返回中提取**思考过程、工具调用、最终答案**三类信息
- 实现多会话管理：多个 session 相互隔离，可随时切换并接续历史；以 **SQLite** 落盘，进程重启后会话不丢失
- 实现上下文管理：最大轮次限制、多轮状态记忆、纯对话追问与带工具追问、上下文超长时的基础压缩（以**可替换的消息重写钩子**组织），并明确每轮 **context 的组装策略**与 memory 召回时机
- 实现可观测性与健壮性：工具调用 trace 与执行日志、基础异常处理（LLM 请求失败、工具执行失败、模型输出格式异常）
- 支持配置文件：配置按「内置默认值 → 配置文件 → 环境变量」三级装载。日常配置写在 `config.yaml` 便于查看与留档，临时切换模型或密钥用环境变量覆盖，不必改文件。配置文件含密钥，默认不进版本库，另附样例文件供参考
- 构建测试用例覆盖上述功能；提供 README 说明运行方式、系统设计、memory 的召回时机与放置方式；记录 AI Prompt 与问题解决过程

**BREAKING**: 无（全新项目，无既有代码或接口需要兼容）

## Capabilities

### New Capabilities

- `agent-loop`: Agent 主循环、流式输出与 LLM 输出解析——接收输入、决策分支、**并发执行多个工具并按调用序号定序回填**、判断收敛条件，以增量方式实时呈现响应，并从模型响应中提取思考过程/工具调用/最终答案
- `tool-registry`: 工具注册与内置工具集——工具的名称/描述/参数 Schema 声明、**名称别名与未知工具兜底**、LLM 驱动的自主调用决策、真实外部 API 工具（博查搜索 / Open-Meteo 天气）的接入与结果截断、工具结果回填
- `session-management`: 会话隔离、切换与 SQLite 持久化——多会话并发独立、随时接续、进程重启后恢复
- `context-management`: 上下文组装与压缩——最大轮次限制、多轮状态记忆、追问支持、超长上下文的基础压缩、memory 召回时机与放置方式
- `observability`: 工具调用 trace、执行日志与异常处理——循环各阶段可追溯，失败可诊断且不中断会话
- `test-suite`: 测试用例——覆盖循环、工具调用、会话隔离、上下文压缩、异常路径，且不依赖真实网络与真实 LLM

### Modified Capabilities

无（项目全新，`openspec/specs/` 下尚无既有能力）

## Impact

- **语言与工具链**: Go 1.25.3（已装于 `C:\Program Files\Go`），`CGO_ENABLED=0`，因此 SQLite 必须使用纯 Go 驱动
- **外部依赖**: `modernc.org/sqlite`（纯 Go SQLite 驱动）与 `github.com/openai/openai-go`（官方 OpenAI Go SDK，v1.12.0）。两者均已实测可拉取、可编译运行。**禁止范围仍然是 agent 框架**（langgraph / openhands / openclaw / PI 这类）——SDK 只承担传输与协议反序列化，循环控制、工具协议、会话隔离、上下文管理全部自研；CLI REPL 与测试仍使用 Go 标准库
- **LLM 接入**: 采用 OpenAI 兼容 Chat Completions 协议，通过 SDK 的 `WithBaseURL` 配合 `OPENAI_BASE_URL` / `OPENAI_API_KEY` / `OPENAI_MODEL` 环境变量切换供应商（DeepSeek、Qwen、Kimi、OpenAI 等通用）。流式与重试由 SDK 提供，但「已在吐字后中途断连」不重试，由应用层处理。**需真实 API Key 才能端到端运行**，测试用例通过本地 mock server 规避该依赖
- **外部服务**: `weather` 接 Open-Meteo——已实测可达、**无需 API Key**、支持中文城市名，需「地理编码 → 预报」两次调用；`search` 接博查 Bocha——已实测可达，需 `BOCHA_API_KEY`。两个工具的 API 端点与 HTTP 客户端均通过依赖注入传入，使测试用 `httptest.Server` 即可完整覆盖解析逻辑，**不发起真实网络请求**
- **配置来源**: 支持 `config.yaml` 配置文件，与环境变量并存。优先级为**环境变量 > 配置文件 > 默认值**；默认位置为工作目录下的 `config.yaml`，可用 `AGENT_CONFIG` 指定其他路径；默认位置的文件不存在时静默回退到默认值，行为与纯环境变量方式一致。为此引入第三个直接依赖 `gopkg.in/yaml.v3`（无间接依赖）
- **新增配置项**: 搜索密钥（搜索必需，环境变量 `BOCHA_API_KEY` 或配置文件 `search.api_key`）。未配置时 `search` 工具不崩溃，而是向模型回填「服务未配置」的说明，其余功能照常可用
- **密钥与版本库**: 实际使用的 `config.yaml` 可能含密钥，已纳入 `.gitignore`；提交 `config.example.yaml` 作为字段参考
- **产物**: 源码仓库 + README + AI Prompt 与问题解决记录；数据库文件 `*.db` 与日志文件需纳入 `.gitignore`，不进版本库
- **交互形态**: CLI REPL，通过 `/new`、`/switch <id>`、`/list`、`/history` 等命令管理会话
- **已知假设**（需求未明确，按此默认执行）:
  - session 的「窗口」以 CLI 会话命令模拟，不做 Web 双窗口界面
  - 上下文压缩采用基础策略（历史裁剪/摘要），不实现复杂压缩
  - 需要初始化 git 仓库以满足「代码链接（github即可）」的提交要求
