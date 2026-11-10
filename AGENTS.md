# Agent Instructions - MeuPreço Codebase

This document provides guidelines, context, and best practices for LLMs working
on the `meupreco` codebase. Follow these instructions strictly to ensure
consistency with existing patterns and project goals.

## Project Overview

MeuPreço is a backend-only Go service that tracks Brazilian supermarket prices
from **NFC-e** (Nota Fiscal de Consumidor Eletrônica) receipts.

- Users submit receipt QR codes/access keys via an API.
- The server fetches the data, parses products and prices, and logs them to a
  shared database.
- It serves price history and supermarket comparisons through a JSON API.

## Core Architecture & Layering

The codebase follows a strict layered architecture:

1. **HTTP Handler (`internal/httpapi`)**: Handles request parsing, validation
   (OpenAPI), authentication middleware, and routing. Handlers should only call
   Services or Stores directly; they do not contain business logic.
2. **Service Layer (`internal/service`, `internal/nfce`, etc.)**: Contains the
   core business logic of the application. This layer orchestrates multiple
   store calls to perform complex operations (e.g., "fetch receipt -> resolve
   identity -> save price").
3. **Store / Repository Layer (`internal/store`)**: The only place where SQL is
   written. It implements domain interfaces and handles database-specific
   details like connection pooling, transaction management, and error
   translation.

## Development Best Practices & Rules

### 1. Scope Control (Strict Focus)

- Only modify code that is directly related to the current task or bug fix.
- Do not refactor unrelated files or "clean up" adjacent logic unless explicitly
  requested by the user.
- If a change requires modifying multiple layers, ensure it follows the flow:
  Handler -> Service -> Store.

### 2. Testing & Linting (Mandatory)

- **Always** run `make lint` before considering any plan as complete or ready
  for review.
- For new features or bug fixes involving logic changes, you must write/update
  tests in the corresponding package's `_test.go` files.
- Use `make test` to verify that all integration and unit tests pass after your
  changes.

> [!NOTE]
> For testing, Makefile contains a shell helper to source .env: tests rely on
> test database

### 3. Code Quality & Redundancy

- **Avoid Duplication**: Before creating a new function or struct, check if an
  existing one in the relevant package can be reused with minor adjustments.
- **DRY (Don't Repeat Yourself)**: If you find yourself copying and pasting
  logic across different services, propose moving that shared logic into
  `internal/domain` or a common service helper.

### 4. Go Best Practices & Patterns

- **Error Handling**: Check every error return value immediately. Wrap errors
  with context using `%w` (e.g.,
  `fmt.Errorf("service: operation failed: %w", err)`).
- **Context Propagation**: Always pass the `context.Context` through all layers
  of calls to ensure proper cancellation and timeouts are respected.
- **Concurrency**: Use goroutines for independent, non-blocking tasks (like
  background GC or enrichment), but always handle their errors via channels or
  shared state safely.
- **Interfaces**: Prefer using interfaces from `internal/domain` when defining
  dependencies in services to facilitate easier testing with mocks/stubs.

### 5. Database & Transactions

- Use the `.Tx()` method on the Store object for any operation that requires
  atomicity across multiple repositories (e.g., creating a user and an initial
  record).
- Never write raw SQL outside of `internal/store`.
- Ensure all database interactions are idempotent where possible, especially in
  ingestion pipelines.

## Key Directories Reference

- `cmd`: Entry points (`server`, `migrate`).
- `internal/domain`: Core entities (User, Product, PriceLog) and repository
  interfaces.
- `internal/httpapi`: HTTP handlers and OpenAPI definitions.
- `internal/store`: PostgreSQL implementation of domain repositories.
- `migrations`: SQL migration files for the database schema.

## Useful Commands

- `make run`: Bootstraps the API server (loads `.env` if present).
- `make lint`: Runs gofmt, go vet, and staticcheck.
- `make test`: Executes all tests in the project.
- `make migrate-up/down`: Manages database schema changes.
