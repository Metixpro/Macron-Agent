---
name: graphify
description: "Use when exploring a big or unfamiliar codebase, or asked how its parts connect or depend on each other: builds a queryable code knowledge graph with the graphify CLI."
version: 1.0.0
category: dev
author: Graphify Labs (adapted for Macron)
license: MIT
---

# graphify: a codebase as a knowledge graph

`graphify` parses a folder of code into a graph (functions, classes, imports, calls), groups it into communities of related code, and answers questions from the graph instead of from dozens of files. In the machine it runs on the code alone, with no model and no API key, so a question costs almost nothing.

## When to use
- A new or big codebase, or several services, where `project_tree` and `code_outline` are not enough.
- Questions about structure, dependencies or relationships across many files: "what calls X", "how does A reach B", "what are the main parts".
- Not for one file or one function: read it, or use `code_outline` and `search_code`.

## First, check that it is there
1. `bash_execute`: `command -v graphify`. If it prints nothing, install it into the user's environment with `install_package` (manager `pip`, package **`graphifyy`** with two y's). It adds the `graphify` command for `bash_execute`.
2. If it cannot be installed (no network, an error), say so in one line and fall back to `project_tree`, `code_outline` and `search_code`. Never pretend a graph was built.
3. Never put an API key here for it, and never run `graphify install`, `graphify global` or the MCP server: none is needed.

## Build the graph (code only, no key, no tokens)
```bash
graphify <folder> --code-only                          # AST of the code → graphify-out/graph.json
GRAPHIFY_BACKEND=none graphify cluster-only <folder>   # → GRAPH_REPORT.md and graph.html, communities named "Community N"
```
- Plain `graphify .` also reads docs, papers and images with a model and stops asking for an API key: always pass `--code-only`. `GRAPHIFY_BACKEND=none` keeps the second step from calling any model.
- A GitHub repository: bring it into the workspace first with `import_repo`, then build on its folder (the machine command has no network).
- A big repository takes a while: run it as a background job and look at its log (`bash_execute` with background, `shell_jobs`).
- After the code changed: `graphify update <folder>` re-extracts the code (no model); rebuild the report with `cluster-only` if you need it.

## Outputs (`graphify-out/`)
1. `GRAPH_REPORT.md`: communities, hubs, the most connected nodes, suggested queries. **Read it first.**
2. `graph.json`: the whole graph; everything below queries it.
3. `graph.html`: an interactive picture of the graph; send it with `send_file` when the user wants to see it.

## Ask the graph
```bash
graphify query "how are tools registered and executed"   # broad context (BFS)
graphify query "how are tools registered" --dfs          # follow one chain deeply
graphify query "…" --budget 5000                         # more nodes when the answer says it was truncated
graphify path "Registry" "Machine"                       # the shortest path between two nodes
graphify explain "Registry"                              # one node and its connections
```
- `query`, `path` and `explain` exit 3 when a name is ambiguous: use the exact label from the report or from a first `query`.
- Names are the code's own (functions, types, packages); quote them exactly.

## Order of work
1. If `graphify-out/graph.json` is missing or the code changed, build it as above.
2. Read `GRAPH_REPORT.md` for the architecture and the communities.
3. Answer with `query`, `path` and `explain`; open the real files (`read_file`, `search_code`) only for what you will quote or change.
4. Say which parts of the answer come from the graph and which you checked in the source. A graph shows structure, not behavior: the edges are extracted or inferred, so confirm what matters.
