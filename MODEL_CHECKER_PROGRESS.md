# 模型巡检功能实现进度

## 已完成的部分

### 阶段 1: 数据库和后端基础 ✅
- [x] 数据库迁移文件 `backend/migrations/202607010003_model_checker.go`
  - 创建了 3 张表：`model_checker_tracked_models`, `model_checker_runs`, `model_checker_run_models`
  - 添加了 `app_settings.model_checker_settings` 字段
- [x] 后端核心文件 `backend/internal/app/model_checker.go`
  - ModelCheckRunner 结构体
  - 配置加载/保存逻辑
  - 基础 API 路由注册（在 `app.go` 中）

### 阶段 2: 核心巡检逻辑 ✅ (新完成)
- [x] `runModelCheck()` - 巡检主流程
  - 加载启用的模型列表
  - 并发检查每个模型
  - 记录运行统计
  - 保存运行记录和详情
- [x] `checkSingleModel()` - 单模型检查
  - 遍历所有 API Keys
  - 调用 CPA `/v1/models` 端点
  - 记录可用的 Key 列表
- [x] `detectChange()` - 变更检测
  - newly_available
  - newly_unavailable
  - keys_changed
  - no_change
- [x] `CheckSingleModel()` - 单模型手动检查接口
- [x] `RunOnce()` - 单次运行模式

### 阶段 3: Daemon 和调度 ✅ (新完成)
- [x] `StartDaemon()` - 启动 Daemon
  - 使用 robfig/cron 库
  - 支持 Asia/Shanghai 时区
  - Cron 表达式验证
- [x] `Stop()` - 停止 Daemon
  - 优雅停止 cron 调度器
  - 清理资源

### 阶段 4: 前端基础界面 ✅ (新完成)
- [x] TypeScript 类型定义（已存在于 `frontend/src/shared/types/api.ts`）
  - ModelCheckerConfig
  - ModelCheckerStatus
  - TrackedModel
  - ModelCheckStats
- [x] API 客户端 `frontend/src/features/model-checker/api/modelCheckerApi.ts`
  - 所有 API 端点的封装
- [x] `ModelCheckConfigView.vue` - 巡检配置页
  - 全局 Daemon 控制
  - 实时日志展示
  - 添加模型到监控
  - 已监控模型配置表格
  - 独立配置：巡检间隔、超时、重试、告警开关
  - 单模型立即检查
  - 全量立即检查
- [x] `ModelStatusView.vue` - 模型状态页
  - 统计卡片（总数、正常、异常、错误）
  - 模型状态表格（带颜色编码背景）
  - 状态标签（绿色/红色/黄色）
  - 可用 Key 数显示
  - 最后检查时间、最后可用时间
- [x] 路由注册（`frontend/src/app/router/index.ts`）
  - `/admin/model-checker` - 巡检配置页
  - `/admin/model-status` - 模型状态页
- [x] 菜单注册（`frontend/src/app/layout/AppShell.vue`）
  - "模型巡检配置"
  - "模型状态"

## 核心实现细节

### 后端 API 端点
所有端点已实现：
- `GET /api/model-checker/settings` - 获取全局配置
- `PUT /api/model-checker/settings` - 更新全局配置
- `POST /api/model-checker/run-once` - 单次运行
- `POST /api/model-checker/start` - 启动 Daemon
- `POST /api/model-checker/stop` - 停止 Daemon
- `GET /api/model-checker/status` - 获取状态
- `GET /api/model-checker/models` - 获取所有监控模型
- `POST /api/model-checker/models` - 添加模型
- `GET /api/model-checker/models/{id}` - 获取单个模型
- `PUT /api/model-checker/models/{id}` - 更新模型配置
- `DELETE /api/model-checker/models/{id}` - 删除模型
- `POST /api/model-checker/models/{id}/check` - 立即检查单个模型
- `POST /api/model-checker/logs/clear` - 清除日志

### 核心功能
1. **模型检查逻辑**
   - 复用现有的 `fetchAvailableModelItems()` 和 `extractAvailableModelItems()`
   - 支持多 API Key 并发检查
   - 超时和重试机制
   - 记录每个模型在哪些 Key 上可用

2. **变更检测**
   - 比较当前状态与上次状态
   - 检测 Key 列表变化
   - 标记 newly_available/newly_unavailable/keys_changed

3. **Daemon 模式**
   - 基于 robfig/cron 的定时调度
   - 支持 Asia/Shanghai 时区
   - 优雅启动/停止

4. **日志系统**
   - 内存日志（最多 300 条）
   - 文件日志（按日期分割）
   - 实时展示

5. **状态可视化**
   - 表格行背景色：绿色(available)、红色(unavailable)、黄色(error)
   - 状态标签：NTag 组件
   - 实时统计

## 待完成的部分

### 阶段 5: 前端完善 (可选增强)
- [ ] 巡检历史查看功能
  - API 端点：`GET /api/model-checker/runs` 和 `GET /api/model-checker/runs/{id}`
  - 历史记录表格/时间线
  - 历史详情抽屉（显示变更高亮）
- [ ] 状态筛选功能
  - 按状态筛选（全部/正常/异常）
  - 按 Provider 筛选
  - 模型 ID 搜索
- [ ] 配置页增强
  - Cron 表达式预览（下次运行时间）
  - 批量设置对话框
- [ ] 详情查看
  - 点击模型查看详情抽屉
  - 显示可用 Key 列表
  - 显示错误信息

### 阶段 6: 测试和优化
- [ ] 单元测试：后端核心逻辑
- [ ] 集成测试：API 端点
- [ ] 前端交互测试
- [ ] 性能优化：并发控制、超时处理

## 验证步骤

### 前置条件
1. 确保已运行数据库迁移
2. 前端安装依赖：`cd frontend && npm install`
3. 后端编译：`cd backend && go build`

### 基础功能验证
1. **访问配置页** (`/admin/model-checker`)
   - 查看全局设置
   - 从可用模型列表中添加模型到监控
   - 为每个模型配置独立参数
   - 查看实时日志

2. **运行巡检**
   - 点击"立即检查所有模型"触发单次巡检
   - 观察日志输出
   - 等待巡检完成

3. **查看状态页** (`/admin/model-status`)
   - 查看统计卡片
   - 验证模型状态表格
   - 验证背景色和状态标签
   - 查看可用 Key 数

4. **启动 Daemon**
   - 在配置页启动 Daemon
   - 验证 Daemon 状态显示
   - 等待自动触发（基于 cron 配置）
   - 停止 Daemon

5. **单模型检查**
   - 在配置页或状态页点击"立即检查"
   - 验证该模型的状态更新

### 变更检测验证
1. 模拟模型可用性变化（修改 API Key 或 CPA 配置）
2. 触发巡检
3. 在状态页验证状态变化
4. （待实现）在历史详情中验证变更类型标记

## 技术栈

### 后端
- Go
- SQLite (数据库)
- robfig/cron/v3 (定时调度)
- 现有的 CPA HTTP 客户端工具

### 前端
- Vue 3 (Composition API)
- TypeScript
- Naive UI (组件库)
- Vue Router

## 注意事项

1. **权限控制**: 所有功能仅限管理员访问
2. **API Key 安全**: 日志中不输出完整的 API Key
3. **时区处理**: 所有时间使用 Asia/Shanghai 时区
4. **并发控制**: 巡检时控制并发数，避免过载
5. **错误处理**: 单个模型检查失败不影响其他模型

## 与账号巡检的区别

| 特性 | 账号巡检 | 模型巡检 |
|------|---------|---------|
| 监控对象 | Codex 账号 | CPA 模型 |
| 配置方式 | 全局统一 | 每个模型独立 |
| 主要关注点 | 配额阈值 | 模型可用性、Key 变化 |
| 状态展示 | 账号列表 | 模型列表（颜色编码） |

## 后续增强方向

1. **告警功能**
   - 邮件/Webhook 通知
   - 模型不可用时告警

2. **更细粒度的监控**
   - 响应时间监控
   - 错误率统计
   - 元数据变更检测

3. **可视化**
   - 模型可用性趋势图
   - 历史记录时间线
   - 导出巡检报告

4. **与用户绑定**
   - 根据用户的 API Key 可见性控制
   - 用户级别的模型监控
