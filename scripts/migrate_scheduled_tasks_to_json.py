#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
将数据库中的 scheduled_tasks / task_executions 迁移到各工作区 tasks.json。

迁移规则：
- 每条 scheduled_task 转成 tasks.json 中一个带 schedule 字段的 OrchestrationTask。
- 该任务的全部 executions 按 execution_id 顺序追加到
  {cwd}/.opennexus/scheduled-executions.jsonl。
- tasks.json 中的 task.executions 仅保留最近 10 条快照，供前端快速展示。

注意：
- 运行前会自动备份数据库到 {db_path}.bak。
- 每个被修改的 tasks.json 会先备份为 tasks.json.bak。
- 幂等：同一任务已存在于 tasks.json（按 id 匹配）时跳过，不覆盖运行时字段。
- 迁移成功后不会删除 scheduled_tasks / task_executions 表，仅迁移数据；
  删表由后续代码发布完成。
"""

import argparse
import json
import os
import shutil
import sqlite3
from datetime import datetime, timezone
from pathlib import Path


def parse_args():
    parser = argparse.ArgumentParser(
        description="将 scheduled_tasks 迁移到工作区 tasks.json"
    )
    parser.add_argument(
        "--db",
        default=os.path.expanduser("~/.openNexus/opennexus.db"),
        help="SQLite 数据库路径",
    )
    parser.add_argument(
        "--dry-run",
        action="store_true",
        help="仅打印迁移计划，不写入文件",
    )
    return parser.parse_args()


def backup_file(path: Path):
    if path.exists():
        bak = path.with_suffix(path.suffix + ".bak")
        shutil.copy2(path, bak)
        return bak
    return None


def load_tasks_json(cwd: str) -> dict:
    p = Path(cwd) / "tasks.json"
    if p.exists():
        with open(p, "r", encoding="utf-8") as f:
            return json.load(f)
    return {"max_parallel": 3, "tasks": []}


def save_tasks_json(cwd: str, data: dict, dry_run: bool):
    p = Path(cwd) / "tasks.json"
    if not dry_run:
        backup_file(p)
        p.parent.mkdir(parents=True, exist_ok=True)
        with open(p, "w", encoding="utf-8") as f:
            json.dump(data, f, ensure_ascii=False, indent=2)
            f.write("\n")
    else:
        print(f"[dry-run] 将写入: {p}")


def append_executions_jsonl(cwd: str, executions: list, dry_run: bool):
    if not executions:
        return
    p = Path(cwd) / ".opennexus" / "scheduled-executions.jsonl"
    if not dry_run:
        p.parent.mkdir(parents=True, exist_ok=True)
        with open(p, "a", encoding="utf-8") as f:
            for e in executions:
                f.write(json.dumps(e, ensure_ascii=False, default=str) + "\n")
    else:
        print(f"[dry-run] 将追加 {len(executions)} 条执行记录到: {p}")


def normalize_status(status: str) -> str:
    """兼容原 scheduled_task 状态到 OrchestrationTask 状态。"""
    if not status:
        return "pending"
    s = status.lower()
    if s in ("success", "succeeded"):
        return "done"
    if s == "running":
        return "running"
    if s == "failed":
        return "failed"
    if s == "skipped":
        return "failed"
    return s


def row_to_task(row: sqlite3.Row, executions: list) -> dict:
    executions_snapshot = executions[-10:] if executions else []
    return {
        "id": str(row["id"]),
        "title": row["name"],
        "detail": row["prompt"],
        "agent_type": row["agent_type"],
        "model_value": row["model_value"] or "",
        "priority": "p1",
        "schedule": {
            "cron_expr": row["cron_expr"],
            "enabled": bool(row["enabled"]),
            "timeout_minutes": row["timeout_minutes"],
            "last_run_at": row["last_run_at"],
            "last_status": normalize_status(row["last_status"] or ""),
            "last_error": row["last_error"] or "",
        },
        "executions": executions_snapshot,
        "status": normalize_status(row["last_status"] or ""),
        "session_id": row["session_id"] or "",
        "db_session_id": row["db_session_id"],
    }


def row_to_execution(row: sqlite3.Row) -> dict:
    return {
        "execution_id": row["execution_id"],
        "status": normalize_status(row["status"]),
        "started_at": row["started_at"],
        "finished_at": row["finished_at"],
        "error": row["error"] or "",
    }


def main():
    args = parse_args()
    db_path = Path(args.db)
    if not db_path.exists():
        print(f"数据库不存在: {db_path}")
        return 1

    if not args.dry_run:
        backup_file(db_path)
        print(f"已备份数据库到: {db_path}.bak")

    conn = sqlite3.connect(str(db_path))
    conn.row_factory = sqlite3.Row

    # 检查表是否存在
    cur = conn.cursor()
    cur.execute(
        "SELECT name FROM sqlite_master WHERE type='table' AND name IN ('scheduled_tasks', 'task_executions', 'workspaces')"
    )
    existing = {r[0] for r in cur.fetchall()}
    if "scheduled_tasks" not in existing:
        print("scheduled_tasks 表不存在，无需迁移。")
        return 0

    # 加载 workspace 映射
    workspaces = {}
    if "workspaces" in existing:
        cur.execute("SELECT id, cwd FROM workspaces")
        for r in cur.fetchall():
            workspaces[r["id"]] = r["cwd"]

    # 加载执行记录
    executions_by_task: dict[int, list] = {}
    if "task_executions" in existing:
        cur.execute("SELECT * FROM task_executions ORDER BY task_id, execution_id")
        for r in cur.fetchall():
            executions_by_task.setdefault(r["task_id"], []).append(row_to_execution(r))

    # 加载定时任务
    cur.execute("SELECT * FROM scheduled_tasks ORDER BY id")
    tasks = cur.fetchall()
    if not tasks:
        print("scheduled_tasks 为空，没有数据需要迁移。")
        return 0

    migrated = 0
    for task in tasks:
        ws_id = task["workspace_id"]
        cwd = task["cwd"]
        if ws_id and ws_id in workspaces:
            cwd = workspaces[ws_id]
        if not cwd:
            print(f"跳过任务 {task['id']}: 无法解析 cwd")
            continue

        executions = executions_by_task.get(task["id"], [])
        new_task = row_to_task(task, executions)

        def_data = load_tasks_json(cwd)
        existing_ids = {t["id"] for t in def_data.get("tasks", [])}
        if new_task["id"] in existing_ids:
            print(f"跳过任务 {task['id']}: tasks.json 中已存在")
            continue

        def_data.setdefault("tasks", []).append(new_task)
        save_tasks_json(cwd, def_data, args.dry_run)
        append_executions_jsonl(cwd, executions, args.dry_run)
        migrated += 1

    print(f"完成迁移 {migrated} 个定时任务。")
    if args.dry_run:
        print("本次为 dry-run，未实际写入文件。")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
