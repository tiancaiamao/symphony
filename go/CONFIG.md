# Symphony 配置指南

## 快速开始（5分钟）

### 1. 修改 WORKFLOW.md

编辑 `/Users/genius/project/symphony/go/WORKFLOW.md`：

```yaml
---
tracker:
  kind: github
  repo: "your-org/your-repo"  # ← 改成你的 GitHub 仓库
  labels:
    - symphony
  active_states:
    - Todo
    - In Progress
    - Merging
    - Rework
  terminal_states:
    - Closed
    - Done
polling:
  interval_ms: 30000
workspace:
  root: ~/.symphony/workspaces
hooks:
  after_create: |
    git clone --depth 1 https://github.com/your-org/your-repo .  # ← 改这里
    git checkout -b {{.TaskID }}
  before_remove: |
    cd {{.Workspace }}
    gh pr merge --squash --delete-branch 2>/dev/null || true
agent:
  max_concurrent_agents: 10
  max_turns: 100
---
```

**需要修改的地方：**
- `repo: "your-org/your-repo"` → 你的 GitHub 仓库
- `git clone ...` 里的仓库地址 → 同样的仓库

### 2. 配置 GitHub 认证

```bash
# 安装 GitHub CLI（如果没有）
brew install gh

# 登录 GitHub
gh auth login

# 验证登录
gh auth status
```

### 3. 启动 Symphony

```bash
cd /Users/genius/project/symphony/go
./bin/symphony
```

服务器会在 `http://localhost:8080` 启动。

### 4. 测试创建任务

```bash
# 创建一个测试任务
symphony task create "[TEST] Test Symphony automation" --desc "
This is a test task to verify Symphony automation works.

Steps:
1. Create a simple file
2. Commit it
3. Create PR

Expected: PR created automatically
"

# 查看任务
symphony task list

# 移到 Todo 触发 AI
symphony task move <task-id> todo

# 查看状态
symphony status

# 打开看板
symphony board
```

---

## 完整配置（可选）

### 5. 配置 Slack 通知

编辑 `~/.symphony/config.yaml`（如果不存在就创建）：

```yaml
# 可选：Slack 通知
hooks:
  after_run: |
    if [ -n "$SLACK_WEBHOOK" ]; then
      PR_URL=$(gh pr view --json url -q .url 2>/dev/null || echo "No PR")
      curl -X POST "$SLACK_WEBHOOK" \
        -H "Content-Type: application/json" \
        -d "{\"text\": \"✅ Task completed: {{.TaskTitle }}\nPR: $PR_URL\"}"
    fi

# 环境变量
env:
  SLACK_WEBHOOK: ${SLACK_WEBHOOK}
```

然后设置环境变量：

```bash
export SLACK_WEBHOOK="https://hooks.slack.com/services/YOUR/WEBHOOK/URL"
```

### 6. 配置 AI Agent

确保你的 AI agent（`ai` 命令）在 PATH 中：

```bash
# 检查 ai 命令是否存在
which ai

# 如果不存在，添加到 PATH
export PATH="$HOME/project/ai/bin:$PATH"

# 或者修改 config.yaml
agent:
  kind: ai
  command: ~/project/ai/bin/ai  # ← 指定完整路径
  args: ["--mode", "rpc"]
```

### 7. 配置 Git

确保 Git 可以自动 push：

```bash
# 配置 Git 用户信息
git config --global user.name "Your Name"
git config --global user.email "your.email@example.com"

# 配置 Git credential helper（避免每次输入密码）
git config --global credential.helper cache
# 或使用 SSH key
```

---

## 高级配置

### 自定义 Hooks

在 `WORKFLOW.md` 中添加自定义 hooks：

```yaml
hooks:
  # 任务创建后：设置环境
  after_create: |
    git clone https://github.com/your-org/your-repo .
    git checkout -b {{.TaskID }}

    # 安装依赖
    npm install  # 或 mix deps.get, pip install

    # 运行数据库迁移（如果需要）
    npm run db:migrate

  # AI 开始前：拉取最新代码
  before_run: |
    git pull origin main
    npm install  # 更新依赖

  # AI 完成后：运行测试 + 创建 PR
  after_run: |
    # 运行测试
    npm test

    # 运行 lint
    npm run lint

    # 创建 PR
    git add . && git commit -m "Fix: {{.TaskTitle }}"
    git push origin {{.TaskID }}
    gh pr create --title "Fix: {{.TaskTitle }}" --body-file WORKPAD.md

    # 通知
    if [ -n "$SLACK_WEBHOOK" ]; then
      curl -X POST "$SLACK_WEBHOOK" -d "{\"text\": \"✅ PR created\"}"
    fi
```

### 配置不同的任务类型

创建不同的 WORKFLOW 文件：

```bash
# Bug fix workflow
cp WORKFLOW.md WORKFLOW-bug.md

# Feature workflow
cp WORKFLOW.md WORKFLOW-feature.md

# 使用不同的 workflow
./bin/symphony WORKFLOW-bug.md
```

---

## 验证配置

运行完整的测试流程：

```bash
# 1. 创建任务
TASK_ID=$(symphony task create "Test automation" | jq -r .id)

# 2. 移到 Todo
symphony task move $TASK_ID todo

# 3. 等待 30 秒（scheduler 轮询）
sleep 30

# 4. 检查状态
symphony task show $TASK_ID

# 5. 查看日志
tail -f ~/.symphony/logs/symphony.log

# 6. 打开看板
symphony board
```

---

## 常见问题

### Q: AI agent 不工作？

```bash
# 检查 ai 命令
which ai
ai --version

# 检查 RPC 模式
ai --mode rpc --help

# 检查 PATH
echo $PATH
```

### Q: GitHub 认证失败？

```bash
# 重新登录
gh auth login

# 检查权限
gh auth status

# 测试 API
gh repo view
```

### Q: Git push 失败？

```bash
# 检查 SSH key
ssh -T git@github.com

# 或使用 HTTPS + token
git config --global credential.helper cache
```

### Q: 任务卡在 Running？

```bash
# 检查 scheduler 日志
tail -f ~/.symphony/logs/scheduler.log

# 检查 agent 进程
ps aux | grep ai

# 重启 Symphony
./bin/symphony
```

---

## 下一步

配置完成后：

1. **创建第一个真实任务**
   ```bash
   symphony task create "[BUG] Fix login timeout"
   ```

2. **观察 AI 工作**
   ```bash
   symphony board  # 打开看板
   ```

3. **Review PR**
   - AI 会创建 PR
   - 你在 GitHub 上 review
   - 添加 comments

4. **AI 自动 address comments**
   - AI 会自动处理你的 comments
   - Push 新的 commits

5. **自动 merge**
   - 你 approve PR
   - 移到 Merging 状态
   - AI 自动 merge

---

## 完整示例

```bash
# 1. 启动 Symphony
cd /Users/genius/project/symphony/go
./bin/symphony &

# 2. 创建 bug fix 任务
symphony task create "[BUG] Users cannot login with SSO" --desc "
Steps to reproduce:
1. Go to login page
2. Click 'Login with SSO'
3. Enter credentials
4. Get 500 error

Expected: Login succeeds
Actual: 500 Internal Server Error

Stack trace:
  at auth.js:123
  at sso.js:456
"

# 3. 触发自动化
symphony task move <task-id> todo

# 4. 打开看板查看进度
open http://localhost:8080/board

# 5. AI 会自动：
#    - 克隆代码
#    - 分析 bug
#    - 实现 fix
#    - 运行测试
#    - 创建 PR
#    - 等待 review
#    - Address comments
#    - 自动 merge

# 6. 你只需要 review PR！
```

---

**配置完成！** 🎉

需要我帮你测试吗？