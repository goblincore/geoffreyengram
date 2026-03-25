# DualMem Benchmark Results

**Task:** Remember the Decision  
**Probe:** What database are we using and why did we choose it?  
**Generated:** 2026-03-25 15:18  

## Responses

### none

Based on the project memory, we are using **PostgreSQL**.

The decision to use PostgreSQL was driven by its:
*   **Robustness and reliability:** It's a mature and well-tested database system.
*   **Feature set:** It offers advanced features like JSONB support, full-text search, and extensibility, which are beneficial for our project's needs.
*   **Open-source nature:** This allows for flexibility and avoids vendor lock-in.
*   **Strong community support:** This ensures ongoing development and readily available help.

### flat

We are using **SQLite** as the database backend.

This choice was made because:
*   It's a **zero-setup** solution, ideal for a CLI tool where users shouldn't need to run a separate database server.
*   While PostgreSQL offers better concurrency, it was deemed **overkill** for a single-user CLI application.
*   **SQLite WAL mode** is enabled to improve read concurrency during search operations.

### dualmem

We are using SQLite as the storage backend. This choice was made because it's a zero-setup solution suitable for a CLI tool, avoiding the need for users to run a separate database server. While PostgreSQL offers better concurrent access, it was deemed overkill for a single-user CLI application. SQLite WAL mode has also been enabled to improve read concurrency during search operations.

## Scores

| Metric | None | Flat | DualMem |
|--------|------|------|--------|
| **Memory Recall** | 1.0 | 4.0 | 5.0 |
| **Relevance** | 1.0 | 5.0 | 5.0 |
| **Prioritization** | 1.0 | 4.0 | 5.0 |
| **Completeness** | 1.0 | 4.0 | 5.0 |
| **No Hallucination** | 1.0 | 5.0 | 5.0 |
| **Average** | **1.0** | **4.4** | **5.0** |
