# Agent Note: Prewalk Fork 的同步与发布边界

Status: implemented

## Problem

生产使用的来源与用量去重补丁、页面 `groups:list` 扩展存在于旧基线的本地修改中。直接更新上游镜像会丢失扩展；只在另一份插件仓库运行集成测试，也不能保护 Fork 日后的自动更新。

## Decision

`prewalk-maintenance` 保存定制实现，个人 Fork 的 `main` 承载默认分支定时任务。自动流程合并上游，不重写历史；验证的是合并后的同一候选提交。测试与镜像 smoke 成功后，流程才推进个人 Fork 分支及同一 digest 的 `latest`、`prewalk-latest` 两个滚动标签，不自动部署服务器。按用户要求，生产 Compose 使用 `ghcr.io/rayenalex/magpie:latest`，不固定 digest；已有容器不会因标签更新而自行换镜像，仍须显式拉取并重建服务。

来源关联继续遵守[宿主合同](../../../../docs/subsystems/provider-plugins.md)：关联不授予权限，内层使用独立服务身份；外层保留审计和客户端 usage，实际消耗只计一次。`groups:list` 继续受页面声明权限约束，只暴露 `id`、`name`、`ref`。

Fork 自带 `TestPluginForwardUsageThroughHost`，通过真实 Bun 插件、LAN 鉴权、独立入口与服务 key、真实用量账本验证单计和来源。外部插件仓库的 53 项实际插件集成属于额外本地兼容性证据，不冒充云端每轮都运行的门禁。

## Alternatives considered

- 自动 rebase 保持线性历史，但维护分支需要强推，容易覆盖并行修复；采用 merge 与非强制推进。
- 只同步源码，再等待 push 触发另一条构建，分工清晰；`GITHUB_TOKEN` 产生的 push 不触发常规 push 工作流，测试与发布必须在同一次流程完成。
- 自动解冲突或服务器 Watchtower 可以减少人工，但超出已确认的维护边界；冲突交给人工，生产继续使用既有镜像。

## Consequences

每六小时的自动检查不等于无人工维护。合并冲突、权限不足和真实回归会阻止更新；GitHub 失败通知取决于账号设置，Fork Actions 和 GHCR 权限也需要首次启用。旧镜像保留，不因失败切换生产。

旧工作区不用于提交，维护分支从个人 Fork 的 `50a38371dfe1dbcba52f44d5c34bb9fd66345eff` 基线独立建立。原有脏文件保留，两个旧补丁通过三方应用迁入；来源恢复优先于新上游的 URL 来源标记，普通请求仍遵守 URL 来源规则。

用户明确限定只验证定制补丁。自动门禁只覆盖来源去重、`groups:list`、同步脚本与镜像构建，不运行上游全仓库测试，不修上游原有问题；官方更新由维护者处理。相邻检查只用于保护本补丁直接影响的消费者。

本地验证与云端发布分别记录。不能把本地构建、模拟 Git 远端或退出码当作 GHCR 已发布、定时任务已启用或生产已部署的证据。
