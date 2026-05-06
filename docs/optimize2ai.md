# AI 代码审查优化记录

> 两轮 AI 代码审查对 gatewayworker-go 的所有修复和优化，按严重程度排序。

---

## 第一轮：核心 Bug 修复 + 基础优化

### 🔴 #1 并发读 map → fatal panic

**文件**: `pkg/gateway/gateway.go` L357

**问题**: `sendToWorker` 释放 RLock 后，无锁访问 `g.workerConns[selectedKey]`。Go runtime 检测到并发 map 读写会直接 **fatal crash**，不可 recover。

```diff
-	return g.sendEncrypted(g.workerConns[selectedKey], encoded)
+	g.mu.RLock()
+	wConn := g.workerConns[selectedKey]
+	g.mu.RUnlock()
+	return g.sendEncrypted(wConn, encoded)
```

---

### 🔴 #2 连接池初始化竞态条件

**文件**: `pkg/gateway_api/gateway_conn.go` L207-211

**问题**: `ensurePool()` 多 goroutine 同时调用时，`pool == nil` 检查非原子操作，可能多次初始化或读到半初始化的 pool。

```diff
-var pool *queryConnPool
-func ensurePool() {
-	if pool == nil {
-		initPool()
-	}
-}
+var (
+	pool     *queryConnPool
+	poolOnce sync.Once
+)
+func ensurePool() {
+	poolOnce.Do(initPool)
+}
```

---

### 🟡 #3 sync.Map 类型断言无 ok 检查

**文件**: `pkg/register/register.go` L218, L236

**问题**: `value.(net.Conn)` 如果类型不匹配会 panic。虽然当前逻辑正确，但缺乏防御性。

```diff
-	value.(net.Conn).Write([]byte(line))
+	if conn, ok := value.(net.Conn); ok {
+		conn.Write([]byte(line))
+	}
```

---

### 🟡 #4 gateway_api 入口无 nil 检查

**文件**: `pkg/gateway_api/gateway_api.go` L29, L39

**问题**: 如果忘记调用 `SetBusinessWorker()`，`bw` 为 nil，所有 API 调用直接 panic。

```diff
 func sendCmd(clientID string, cmd uint8, message []byte, extData string) {
+	if bw == nil {
+		return
+	}
 	...
```

---

### 🟢 #5 sendEncrypted 热路径减少分配 (×5 处)

**文件**: `gateway.go`, `business_worker.go`, `gateway_conn.go`, `gateway_worker_handler.go`

**问题**: 每次发送消息都 `make(4) + append(lenBuf, encrypted...)`，产生 2 次堆分配。这是**每条消息**都经过的热路径。

```diff
-	lenBuf := make([]byte, 4)
-	binary.BigEndian.PutUint32(lenBuf, uint32(len(encrypted)))
-	_, err = conn.Write(append(lenBuf, encrypted...))
+	buf := make([]byte, 4+len(encrypted))
+	binary.BigEndian.PutUint32(buf[:4], uint32(len(encrypted)))
+	copy(buf[4:], encrypted)
+	_, err = conn.Write(buf)
```

**效果**: 每条消息减少 1 次堆分配。高并发下（10 万 msg/s）减少约 10 万次/秒的 GC 压力。

---

## 第二轮：锁优化 + 内存安全

### 🔴 #6~#8 广播方法持锁执行 I/O

**文件**: `gateway_worker_handler.go` — `handleSendToAll`, `handleSendToUID`, `handleSendToGroup`

**问题**: 持 RLock 调用 `cc.Conn.Write()`。WebSocket 的 Write 可能因客户端慢消费而阻塞数秒。持锁期间所有写操作（BindUID、JoinGroup、LeaveGroup）全部排队等待。

**万级连接广播时锁可能持有数秒**，导致整个 Gateway 卡顿。

```diff
-	g.mu.RLock()
-	defer g.mu.RUnlock()
-	for _, cc := range g.clientConns {
-		cc.Conn.Write(data.Body)
-	}
+	var targets []ClientConn
+	g.mu.RLock()
+	for _, cc := range g.clientConns {
+		targets = append(targets, cc.Conn)
+	}
+	g.mu.RUnlock()
+	// 锁外批量写
+	for _, conn := range targets {
+		conn.Write(data.Body)
+	}
```

**效果**: 锁持有时间从 O(N×网络延迟) 降到 O(N×指针拷贝) ≈ 微秒级。

---

### 🟢 #9 心跳包重复编码

**文件**: `pkg/gateway/gateway.go` — `pingWorkerLoop`

**问题**: 每 25 秒对每个 Worker 都 `NewEmptyData()` + `Encode()`，生成完全一样的心跳包。

```diff
+	pingGd := protocol.NewEmptyData()
+	pingGd.Cmd = protocol.CmdPing
+	pingPayload := protocol.Encode(pingGd)
+
 	for _, wconn := range g.workerConns {
-		gd := protocol.NewEmptyData()
-		gd.Cmd = protocol.CmdPing
-		g.sendEncrypted(wconn, protocol.Encode(gd))
+		g.sendEncrypted(wconn, pingPayload)
 	}
```

---

### 🟡 #10 TextProtocol.Encode append 污染

**文件**: `pkg/gateway/protocol_text.go`

**问题**: `append(data, '\n')` 如果 `data` 底层数组有剩余 capacity，会直接改写调用方的底层数据，导致隐蔽的数据损坏。

```diff
-	return append(data, '\n')
+	buf := make([]byte, len(data)+1)
+	copy(buf, data)
+	buf[len(data)] = '\n'
+	return buf
```

---

### 🟡 #11 TCPClientConn.Read 缓冲区内存泄漏

**文件**: `pkg/gateway/client_conn.go`

**问题**: `c.buf = c.buf[n:]` 反复 reslice 后，底层数组前端已消费的字节永远不会被 GC 回收。长时间运行的 TCP 连接会逐渐泄漏内存。

```diff
-	c.buf = c.buf[n:]
+	remaining := len(c.buf) - n
+	if remaining == 0 {
+		c.buf = c.buf[:0]
+	} else {
+		newBuf := make([]byte, remaining)
+		copy(newBuf, c.buf[n:])
+		c.buf = newBuf
+	}
```

---

### 🟡 #12 gateway_sdk 认证加密忽略错误

**文件**: `pkg/gateway_sdk/gateway_sdk.go` L117

**问题**: `encrypted, _ := crypto.Encrypt(...)` 忽略了加密错误，如果 aesKey 有问题会发送空数据。

```diff
-	encrypted, _ := crypto.Encrypt(protocol.Encode(authData), c.aesKey)
+	encrypted, err := crypto.Encrypt(protocol.Encode(authData), c.aesKey)
+	if err != nil {
+		conn.Close()
+		return nil, fmt.Errorf("encrypt auth failed: %w", err)
+	}
```

---

### 🟢 #13 gateway_sdk 3 处 append 优化

**文件**: `pkg/gateway_sdk/gateway_sdk.go` — `getConn`, `sendToGateway`, `sendAndRecv`

与 #5 相同的 `append(lenBuf, encrypted...)` 模式，统一改为 `make+copy`。

---

## 修复统计

| 等级 | 数量 | 说明 |
|---|---|---|
| 🔴 必修（会导致 crash/卡死） | **5** | 并发 map panic、竞态初始化、锁下 I/O 阻塞 |
| 🟡 应修（防御性 / 内存安全） | **5** | 类型断言、nil 检查、append 污染、内存泄漏、错误处理 |
| 🟢 优化（性能提升） | **3** | 热路径分配、心跳复用、gateway_sdk 一致性 |
| **总计** | **13** | |

## 未改动的项（不值得改）

| 代码 | 理由 |
|---|---|
| `addrStr` 用 `fmt.Sprintf` | 非热路径，改 strconv 收益 < 1% |
| `protocol.Encode` 每次 make | 不同调用编码不同数据，必须独立分配 |
| register.go 字符串 `+` | 认证时执行一次 |
| `*net.TCPAddr` 断言不加 ok | TCP accept 的连接一定是 TCPAddr |
| `FrameProtocol.Encode` 的 append | 客户端协议编码，频率远低于内部通讯 |
| `handleSelect` 锁内 json.Marshal | 纯 CPU 操作不涉及 I/O，不阻塞 |
| 用户回调不加 recover | Go 惯例让 panic 暴露，方便调试 |
