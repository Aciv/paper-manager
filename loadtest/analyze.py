#!/usr/bin/env python3
"""Join round timestamps with process samples without mixing API and driver RSS."""
import argparse
from datetime import datetime
import json
from pathlib import Path


def instant(raw):
    return datetime.fromisoformat(raw.replace("Z", "+00:00")).timestamp()


def resource_window(samples, start, end, group):
    rows = [row for row in samples if start <= instant(row["time"]) <= end and row[group]]
    if not rows:
        return {"samples": 0}
    peak = max(sum(process["rss_mib"] for process in row[group]) for row in rows)
    peak_fds = max(sum(process["fds"] for process in row[group]) for row in rows)
    first = {item["pid"]: item["cpu_seconds"] for item in rows[0][group]}
    last = {item["pid"]: item["cpu_seconds"] for item in rows[-1][group]}
    elapsed = instant(rows[-1]["time"]) - instant(rows[0]["time"])
    cpu = sum(last[pid] - value for pid, value in first.items() if pid in last)
    return {"samples": len(rows), "peak_rss_mib": round(peak, 2), "peak_fds": peak_fds,
            "mean_cpu_cores": round(cpu / elapsed, 2) if elapsed else None,
            "cpu_note": "CPU deltas only include PIDs present at both ends; Python worker churn may be omitted"}


def analyze(root):
    result = json.loads((root / "results.json").read_text())
    samples = [json.loads(line) for line in (root / "resources.jsonl").read_text().splitlines()]
    rows = []
    for row in result["Rounds"]:
        resources = {name: resource_window(samples, instant(row["StartedAt"]), instant(row["FinishedAt"]), name)
                     for name in ("api", "python", "driver")}
        rows.append({"documents": row["Documents"], "clients": row["Clients"], **resources})
    output = {"rounds": rows, "all_run": {
        name: resource_window(samples, float("-inf"), float("inf"), name)
        for name in ("api", "python", "driver")}}
    (root / "resources-summary.json").write_text(json.dumps(output, indent=2) + "\n")
    text = ["\n## API 资源采样\n\n", "| 论文数 | 并发 | API 峰值 MiB | API 平均 CPU 核 | API 峰值 fd |\n",
            "|---:|---:|---:|---:|---:|\n"]
    for row in rows:
        api = row["api"]
        text.append(f"| {row['documents']} | {row['clients']} | {api.get('peak_rss_mib', '—')} | "
                    f"{api.get('mean_cpu_cores', '—')} | {api.get('peak_fds', '—')} |\n")
    text.append("\n每秒采样，CPU 的 1.0 表示平均占满一个逻辑核；RSS 与压测驱动分开统计。"
                "未采集 API 进程内 sql.DB 等待计数，也未安装 pg_stat_statements，数据库计数不是 SQL 请求次数。\n")
    summary = root / "summary.md"
    original = summary.read_text().split("\n## API 资源采样", 1)[0]
    summary.write_text(original + "".join(text))
    return output


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("run", type=Path)
    args = parser.parse_args()
    analyze(args.run)
    print(args.run / "resources-summary.json")


if __name__ == "__main__":
    main()
