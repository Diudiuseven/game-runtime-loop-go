# game-runtime-loop-go

一套可复现的游戏运行时主循环：按**固定时间步**推进逻辑，把逻辑更新与外部渲染 /
采样节奏解耦。整条链路纯 Go 实现，不依赖任何外部引擎，`go test` 与
`go run ./cmd/demo` 即可在本地复现与验证。

## 时间模型

- **固定步长**：逻辑只在整数个 `Config.Step` 上推进。tick `N` 的逻辑时间恒为
  `N * Step`，与真实时钟、渲染节奏、单次循环耗时无关。
- **外部驱动**：调用方以任意节奏调用 `Advance(elapsed)` 注入真实流逝时间。
  流逝时间先乘以时间缩放，再以整数纳秒累加进累加器（同一输入序列在任何机器上
  结果一致），随后按整步消费。
- **确定性**：逻辑状态只是「已执行的步序列 + 各步输入」的纯函数。相同输入序列
  无论分块方式如何（见 `TestDeterministicTrajectory` 与 `cmd/demo`）都得到一致
  的状态轨迹。

## 追帧与过载

单次 `Advance` 最多执行 `Config.MaxCatchUp` 步，补跑不会无限堆积。超出上界的
时间按 `Config.Overflow` 处理，结果在 `Result` 中**可区分**地上报，绝不静默丢失：

| 策略 | 行为 | 上报 |
| --- | --- | --- |
| `OverflowDrop` | 丢弃超出部分的整步时间，模拟向前跳过 | `Result.Dropped` |
| `OverflowDefer` | 保留至多 `MaxCatchUp` 步的有界积压，模拟以有界延迟落后真实时间，超出积压上界的部分丢弃；后续 `Advance`（含 `Advance(0)`）继续消化积压 | `Result.Backlog` / `Result.Dropped` |

突发请求被上界截断并上报，空闲期 `Advance(0)` 不产生新步（仅消化既有积压）。

## 暂停与时间缩放

- `SetPaused(true)`：逻辑时间冻结。暂停期间 `Advance` 不执行步、**不累积**时间，
  恢复后不会出现追帧突发。边界明确：缩放 / 暂停只影响调用之后流逝的时间。
- `SetTimeScale(s)`：`s >= 0`。之后流逝的时间先乘 `s` 再入累加器；步长本身恒为
  `Config.Step`，缩放后的推进仍落在固定的步长格点上。

## 调度语义

- 系统通过 `Register` 注册，**注册顺序即执行顺序**，不受其他任何因素影响。
- 每个系统执行前先对状态做快照；系统返回错误或发生 panic 时，状态回滚到该系统
  执行前，后续系统（或下一步）永远看不到半更新状态。panic 被 recover 并以
  `SystemError{Panic: true}` 上报。
- 失败后的行为由 `Config.ErrorPolicy` 决定：
  - `PolicyIsolate`：回滚失败系统，继续执行后续系统；
  - `PolicyAbortStep`：回滚失败系统，跳过本步剩余系统。
- 所有失败按顺序收集在 `Result.Errors` 中。

## 热更新

- `Swap(sys)` 请求替换同名系统。新实现若实现 `Validatable` 接口会先被校验，
  校验失败则请求被拒绝、旧版本继续运行（回退到上一个可用版本，循环不中断）。
- 合法的替换在**步边界**提交（`Result.Swaps` 上报），处于中间状态的步对外不可见，
  替换前后逻辑时间与状态严格对齐。

## 快照与恢复

`Snapshot()` 捕获 tick、累加器、暂停标志、时间缩放与状态深拷贝；`Restore(snap)`
后从同一点继续推进，后续轨迹与不中断时完全一致
（`TestSnapshotRestore`）。调度表本身不属于快照。

## 配置项

| 字段 | 含义 | 约束 |
| --- | --- | --- |
| `Step` | 固定逻辑步长 | `> 0` |
| `MaxCatchUp` | 单次 `Advance` 最大补跑步数 | `> 0` |
| `Overflow` | 过载策略：`OverflowDrop` / `OverflowDefer` | — |
| `ErrorPolicy` | 系统异常策略：`PolicyIsolate` / `PolicyAbortStep` | — |

## 状态接口

```go
type State interface {
    Clone() State        // 深拷贝，用于回滚与快照
    CopyFrom(src State)  // 从拷贝恢复
}
```

## 验证

```sh
go test ./...    # 固定步长确定性、追帧上界、暂停/缩放、异常隔离、热更新对齐、快照恢复、过载
go run ./cmd/demo # 同一逻辑输入在两种真实时间节奏下产出一致的状态哈希
```
