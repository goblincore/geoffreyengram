#!/usr/bin/env node
// benchmarks/contextual/adapters/unravel.js
//
// Adapter for UnravelAI — called by the Go benchmark runner.
// Two modes:
//   --mode search  → library-level graph search (for IR metrics)
//   --mode consult → MCP-level consult (for report quality)
//
// Usage:
//   node unravel.js --mode search --corpus /path/to/repo --query "where is auth?"
//   node unravel.js --mode consult --corpus /path/to/repo --query "where is auth?"

import { GraphBuilder } from '/tmp/unravelai/unravel-mcp/core/graph-builder.js';
import { SearchEngine, queryGraphForFiles } from '/tmp/unravelai/unravel-mcp/core/search.js';
import { parseCode } from '/tmp/unravelai/unravel-mcp/core/ast-engine-ts.js';
import fs from 'fs';
import path from 'path';
import { execSync } from 'child_process';

const args = process.argv.slice(2);
function getArg(name) {
  const idx = args.indexOf('--' + name);
  return idx >= 0 && idx + 1 < args.length ? args[idx + 1] : null;
}

const mode = getArg('mode') || 'search';
const corpusPath = getArg('corpus');
const query = getArg('query');

if (!corpusPath || !query) {
  console.error(JSON.stringify({ error: 'Missing --corpus or --query' }));
  process.exit(1);
}

// Recursively find source files
function findSourceFiles(dir, exts = ['.ts', '.tsx', '.js', '.jsx', '.go', '.py', '.rs']) {
  const results = [];
  try {
    const entries = fs.readdirSync(dir, { withFileTypes: true });
    for (const entry of entries) {
      const full = path.join(dir, entry.name);
      if (entry.isDirectory()) {
        if (['node_modules', '.git', 'dist', 'build', '.next', 'vendor'].includes(entry.name)) continue;
        results.push(...findSourceFiles(full, exts));
      } else if (exts.some(ext => entry.name.endsWith(ext))) {
        results.push(full);
      }
    }
  } catch (e) { /* skip unreadable dirs */ }
  return results;
}

async function runSearch() {
  const start = Date.now();

  // Build knowledge graph
  const builder = new GraphBuilder(path.basename(corpusPath));
  const files = findSourceFiles(corpusPath);

  for (const filePath of files) {
    const relPath = path.relative(corpusPath, filePath);
    try {
      const code = fs.readFileSync(filePath, 'utf-8');
      // Try AST analysis for TS/JS files
      if (filePath.match(/\.(ts|tsx|js|jsx)$/)) {
        try {
          const analysis = parseCode(code, filePath);
          builder.addFileWithAnalysis(relPath, analysis, null);
        } catch {
          builder.addFile(relPath, '', [], 'unknown');
        }
      } else {
        builder.addFile(relPath, '', [], 'unknown');
      }
    } catch {
      builder.addFile(relPath, '', [], 'unknown');
    }
  }

  const graph = builder.build();

  // Query the graph
  const searchResults = queryGraphForFiles(graph, query, 10);
  const latency = Date.now() - start;

  const results = (searchResults || []).map(r => ({
    path: r.filePath || r.file || r.name || '',
    score: r.score || r.relevance || 0,
  }));

  console.log(JSON.stringify({ results, latency_ms: latency, api_calls: 0 }));
}

async function runConsult() {
  // For MCP consult, we spawn the MCP server and send a tool call
  const start = Date.now();

  try {
    // Try MCP stdio approach
    const { spawn } = await import('child_process');
    const mcp = spawn('node', ['/tmp/unravelai/unravel-mcp/index.js'], {
      stdio: ['pipe', 'pipe', 'pipe'],
      cwd: corpusPath,
    });

    const toolCall = {
      jsonrpc: '2.0',
      id: 1,
      method: 'tools/call',
      params: {
        name: 'unravel.consult',
        arguments: { query, scope: corpusPath },
      },
    };

    // Initialize MCP
    const initMsg = {
      jsonrpc: '2.0',
      id: 0,
      method: 'initialize',
      params: {
        protocolVersion: '2024-11-05',
        capabilities: {},
        clientInfo: { name: 'benchmark', version: '1.0.0' },
      },
    };

    let response = '';
    mcp.stdout.on('data', (data) => { response += data.toString(); });

    // Send init, wait, send tool call
    mcp.stdin.write(JSON.stringify(initMsg) + '\n');

    await new Promise(resolve => setTimeout(resolve, 2000));
    mcp.stdin.write(JSON.stringify(toolCall) + '\n');
    await new Promise(resolve => setTimeout(resolve, 10000));

    mcp.kill();

    const latency = Date.now() - start;

    // Try to parse the last JSON-RPC response
    const lines = response.split('\n').filter(l => l.trim());
    let report = '';
    for (const line of lines.reverse()) {
      try {
        const parsed = JSON.parse(line);
        if (parsed.result?.content) {
          report = parsed.result.content.map(c => c.text || '').join('\n');
          break;
        }
      } catch { continue; }
    }

    console.log(JSON.stringify({ report, latency_ms: latency }));
  } catch (e) {
    const latency = Date.now() - start;
    console.log(JSON.stringify({ report: '', latency_ms: latency, error: e.message }));
  }
}

if (mode === 'search') {
  runSearch().catch(e => {
    console.error(JSON.stringify({ error: e.message }));
    process.exit(1);
  });
} else if (mode === 'consult') {
  runConsult().catch(e => {
    console.error(JSON.stringify({ error: e.message }));
    process.exit(1);
  });
} else {
  console.error(JSON.stringify({ error: `Unknown mode: ${mode}` }));
  process.exit(1);
}
