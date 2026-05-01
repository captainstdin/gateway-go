package gateway

import (
	"math/rand"
	"sync"
)

// RouterMode 路由模式
type RouterMode string

const (
	RouterRandom           RouterMode = "random"
	RouterLeastConnections RouterMode = "least_connections"
)

// Router 路由器，将客户端请求路由到某个 Worker 连接
type Router struct {
	mode                RouterMode
	leastConnectionsMap map[string]int // workerKey -> 当前客户端连接数
	mu                  sync.RWMutex
}

// NewRouter 创建路由器
func NewRouter(mode RouterMode) *Router {
	return &Router{
		mode:                mode,
		leastConnectionsMap: make(map[string]int),
	}
}

// SelectWorker 根据路由策略选择一个 worker
// workerKeys: 所有可用 worker 的 key 列表
// boundKey: 客户端之前绑定的 worker key（如果有）
// 返回选中的 workerKey
func (r *Router) SelectWorker(workerKeys []string, boundKey string) string {
	if len(workerKeys) == 0 {
		return ""
	}

	// 如果已绑定且该 worker 仍然在线，继续使用
	if boundKey != "" {
		for _, k := range workerKeys {
			if k == boundKey {
				return boundKey
			}
		}
	}

	// 按模式选择
	switch r.mode {
	case RouterLeastConnections:
		return r.selectLeastConnections(workerKeys)
	default:
		return r.selectRandom(workerKeys)
	}
}

// selectRandom 随机选择
func (r *Router) selectRandom(workerKeys []string) string {
	return workerKeys[rand.Intn(len(workerKeys))]
}

// selectLeastConnections 选择连接数最少的 worker
func (r *Router) selectLeastConnections(workerKeys []string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	minKey := workerKeys[0]
	minCount := r.leastConnectionsMap[minKey]

	for _, key := range workerKeys[1:] {
		count := r.leastConnectionsMap[key]
		if count < minCount {
			minCount = count
			minKey = key
		}
	}
	return minKey
}

// OnWorkerConnected worker 上线，初始化计数
func (r *Router) OnWorkerConnected(workerKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.leastConnectionsMap[workerKey]; !exists {
		r.leastConnectionsMap[workerKey] = 0
	}
}

// OnWorkerDisconnected worker 下线，移除计数
func (r *Router) OnWorkerDisconnected(workerKey string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.leastConnectionsMap, workerKey)
}

// IncrementCount 客户端路由到某 worker 时，计数+1
func (r *Router) IncrementCount(workerKey string) {
	if r.mode != RouterLeastConnections {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.leastConnectionsMap[workerKey]++
}

// DecrementCount 客户端断开时，计数-1
func (r *Router) DecrementCount(workerKey string) {
	if r.mode != RouterLeastConnections {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.leastConnectionsMap[workerKey] > 0 {
		r.leastConnectionsMap[workerKey]--
	}
}
