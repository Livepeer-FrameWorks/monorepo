import Foundation

actor AuthRefreshCoordinator {
  private var pending: (id: UUID, task: Task<Void, Error>)?
  private var closed = false

  func run(_ operation: @escaping @MainActor () async throws -> Void) async throws {
    guard !closed else { throw CancellationError() }
    if let pending {
      return try await pending.task.value
    }
    let task = Task { try await operation() }
    let id = UUID()
    pending = (id, task)
    defer {
      if pending?.id == id { pending = nil }
    }
    try await task.value
  }

  func close() async {
    closed = true
    let current = pending
    current?.task.cancel()
    _ = try? await current?.task.value
    if pending?.id == current?.id { pending = nil }
  }

  func open() { closed = false }

  func finishPending() async {
    _ = try? await pending?.task.value
  }
}
