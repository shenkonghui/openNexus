---
name: excel
description: Use when reading, writing, editing, analyzing, or transforming Excel (.xlsx/.xlsm/.csv/.tsv) files. Covers pandas for analysis, openpyxl for cell-level editing/formatting/formulas, and LibreOffice headless for formula recalculation.
---

# Excel 文件处理指南

当用户要求创建、编辑、分析或可视化 Excel 文件（.xlsx / .xlsm / .csv / .tsv）时，按本指南操作。

## 库选型

| 库 | 用途 | 是否需要装 Excel | Linux 可用 |
|------|------|------|------|
| pandas | 行列聚合、分析、CSV/TSV 读写 | 否 | 是 |
| openpyxl | 单元格级编辑、格式、公式、图表、编辑已有 xlsx | 否 | 是 |
| xlsxwriter | 只写新建 xlsx，富格式/图表，最快写路径 | 否 | 是 |
| xlwings | 驱动真实 Excel / 跑 VBA 宏 / 公式重算 | 是（仅 Win/Mac） | 否 |

依赖安装（优先 uv，回退 pip）：

```bash
uv pip install openpyxl pandas xlsxwriter 2>/dev/null || python3 -m pip install openpyxl pandas xlsxwriter
```

## 工作流

1. 先确认文件类型与目标：创建 / 编辑 / 分析 / 可视化。
2. 分析用 pandas；编辑/格式化/公式用 openpyxl；新建+富格式用 xlsxwriter。
3. 派生值优先用 Excel 公式而非 Python 算后硬编码，保持工作簿动态。
4. 交付前尽可能重算公式，使缓存值写入工作簿（见下）。
5. 若布局/视觉效果重要，渲染后人工复核。
6. 保留稳定文件名，清理中间文件。

## 读取与分析

```python
import pandas as pd
# 默认读第一个 sheet
df = pd.read_excel('file.xlsx')
# 指定 sheet
df = pd.read_excel('file.xlsx', sheet_name='Sheet2')
# 全部 sheet
sheets = pd.read_excel('file.xlsx', sheet_name=None)
```

## 编辑与格式化（openpyxl）

```python
from openpyxl import load_workbook
wb = load_workbook('file.xlsx')  # 保留公式与格式
ws = wb.active
ws['C2'] = '=A2+B2'  # 写公式
wb.save('file.xlsx')
```

## 公式重算（关键）

openpyxl / pandas 不计算公式，只读 Excel 上次保存时缓存的值。脚本生成的 xlsx 没有缓存值。
若环境有 LibreOffice，用 headless 重算后再交付：

```bash
libreoffice --headless --calc --convert-to xlsx:"Calc MS Excel 2007 XML" --outdir <out> <file>
```

或调用本仓库 scripts/recalc.py（若存在）。无 LibreOffice 时，对纯计算结果可直接用 openpyxl 写值，并在交付说明里提示用户首次打开时 Excel 会自动重算。

## 公式编写约定

- 用公式而非硬编码值，保持工作簿动态可复算。
- 避免动态数组函数：FILTER / XLOOKUP / SORT / SEQUENCE（部分 Excel 版本不支持）。
- 避免 volatile 函数：INDIRECT / OFFSET（除非必要，会触发全表重算）。
- 复杂逻辑用辅助列拆分，保持公式可读。

## 安全

- 公式注入（CWE-1236）：写入以 `= + - @` 开头的字符串时，前缀加 `'` 强制文本：
  ```python
  def safe_cell(v):
      if isinstance(v, str) and v and v[0] in ('=','+','-','@','\t','\r'):
          return "'" + v
      return v
  ```
- 解析来源不明的 xlsx 时防 XXE（openpyxl 默认安全，避免用 xml.etree 直接解析共享字符串）。

## 大文件 / 省 token

- 不要把整表塞进上下文。先读结构（sheet 名、列名、行数、前几行），按需用 pandas/DuckDB 查询。
- 超大表用 chunksize 流式读取：
  ```python
  for chunk in pd.read_excel('big.xlsx', chunksize=10000):
      process(chunk)
  ```
- 仅向用户汇报聚合结果与摘要，不回显原始行数据。
