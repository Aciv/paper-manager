package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type report struct {
	RunID                 string
	StartedAt, FinishedAt time.Time
	Options               options
	Boundary              string
	Rounds                []roundResult
	Imports               []importResult
	CorrectnessErrors     []string
}

func percentiles(values []float64) latency {
	if len(values) == 0 {
		return latency{}
	}
	sort.Float64s(values)
	at := func(p float64) float64 { return values[max(0, int(math.Ceil(float64(len(values))*p))-1)] }
	return latency{P50: at(.5), P95: at(.95), P99: at(.99), Max: values[len(values)-1]}
}

func (r report) markdown() string {
	var out strings.Builder
	fmt.Fprintf(&out, "# Go 后端压测\n\nRun: `%s`\n\n", r.RunID)
	out.WriteString("真实 HTTP/Go/PostgreSQL/Redis/文件系统；主要负载不调用 Python。语料为合成结果，PDF 信息声明为合成多页、源 PDF 为传输用单页文件。\n\n")
	out.WriteString("每个文档仅一个写入者；不同 notes 可并行写，读者可与写者重叠。并发数是同时在途工作流数，不是论文总数。\n\n")
	out.WriteString("| 论文数 | 并发客户端 | 写入者 | 请求数 | req/s | p50 ms | p95 ms | p99 ms | 错误 |\n")
	out.WriteString("|---:|---:|---:|---:|---:|---:|---:|---:|---:|\n")
	for _, v := range r.Rounds {
		fmt.Fprintf(&out, "| %d | %d | %d | %d | %.1f | %.2f | %.2f | %.2f | %d |\n",
			v.Documents, v.Clients, v.Writers, v.Requests, v.RPS, v.Latency.P50, v.Latency.P95, v.Latency.P99, v.Errors)
	}
	out.WriteString("\n## 500 篇各接口\n\n| 并发 | 操作 | req/s | p95 ms | p99 ms | 错误 |\n|---:|---|---:|---:|---:|---:|\n")
	for _, v := range r.Rounds {
		if v.Documents == 500 {
			var keys []string
			for key := range v.Operations {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				op := v.Operations[key]
				fmt.Fprintf(&out, "| %d | %s | %.1f | %.2f | %.2f | %d |\n",
					v.Clients, key, op.RPS, op.Latency.P95, op.Latency.P99, op.Errors)
			}
		}
	}
	out.WriteString("\n## 导入\n\n入队耗时、文件入库和后续解析是不同阶段。重复输入返回 duplicate 属于预期成功。\n\n")
	for _, v := range r.Imports {
		fmt.Fprintf(&out, "- %s：%d 项，接收 p95 %.2fms，入库完成 %.2fs，accepted=%d duplicate=%d rejected=%d failed=%d。\n",
			v.Name, v.Total, v.Acceptance.P95, v.CompletionSeconds, v.Accepted, v.Duplicate, v.Rejected, v.Failed)
	}
	for _, err := range r.CorrectnessErrors {
		fmt.Fprintf(&out, "\n- 正确性错误：%s\n", err)
	}
	out.WriteString("\n原始按接口状态/延迟和数据库采样见 results.json，Go/Python/压测进程资源见 resources.jsonl。" +
		"短轮次为闭环负载，不能等同线上 p99 或持续到达率测试；客户端与服务同机，共享 CPU。\n")
	return out.String()
}
