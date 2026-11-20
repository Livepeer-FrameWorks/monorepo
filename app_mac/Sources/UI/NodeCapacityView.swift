import SwiftUI

struct NodeCapacityView: View {
  let nodeId: String
  @State private var capacity: EdgeNodeCapacity?
  @State private var egress = ""
  @State private var ingress = ""
  @State private var busy = false
  @State private var error: String?
  private let service = NodeCapacityService()

  var body: some View {
    VStack(alignment: .leading, spacing: 8) {
      HStack {
        Text("Bandwidth capacity").font(.subheadline.bold())
        Spacer()
        Button("Reload") { Task { await load() } }.disabled(busy)
      }
      if let capacity {
        Text("Egress: \(formatted(capacity.egressBudgetBytesPerSec)) Mbit/s · \(capacity.egressSource.lowercased())")
        Text("Ingress: \(formatted(capacity.ingressBudgetBytesPerSec)) Mbit/s · \(capacity.ingressSource.lowercased())")
        Text(capacity.applicationStatus)
          .foregroundStyle(capacity.appliedRevision == capacity.revision ? Color.tnGreen : Color.tnOrange)
        TextField("Egress Mbit/s (default: 1000)", text: $egress)
        TextField("Ingress Mbit/s (default: 1000)", text: $ingress)
        Text("Leave blank for default 1 Gbit/s. New traffic uses up to 85%.")
          .font(.caption2).foregroundStyle(.secondary)
        HStack {
          Button("Save capacity") { Task { await save(capacity) } }.disabled(busy)
          Button("Use defaults") { egress = ""; ingress = "" }.disabled(busy)
        }
      } else if !busy {
        Text("Capacity unavailable").foregroundStyle(.secondary)
      }
      if let error { Text(error).foregroundStyle(Color.tnOrange).textSelection(.enabled) }
    }
    .font(.caption)
    .textFieldStyle(.roundedBorder)
    .padding()
    .background(Color.tnAccent.opacity(0.05))
    .clipShape(RoundedRectangle(cornerRadius: 8))
    .task(id: nodeId) { await load() }
  }

  private func formatted(_ bytes: Double) -> String {
    (bytes / 125_000).formatted(.number.precision(.fractionLength(0...3)))
  }

  @MainActor private func apply(_ value: EdgeNodeCapacity) {
    capacity = value
    egress = value.egressSource == "DEFAULT" ? "" : String(value.egressBudgetBytesPerSec / 125_000)
    ingress = value.ingressSource == "DEFAULT" ? "" : String(value.ingressBudgetBytesPerSec / 125_000)
  }

  @MainActor private func load() async {
    busy = true
    error = nil
    defer { busy = false }
    do { apply(try await service.fetch(nodeId: nodeId)) }
    catch { self.error = error.localizedDescription }
  }

  @MainActor private func save(_ current: EdgeNodeCapacity) async {
    busy = true
    error = nil
    defer { busy = false }
    do {
      apply(try await service.set(nodeId: nodeId, revision: current.revision, egressMbps: egress, ingressMbps: ingress))
    } catch { self.error = error.localizedDescription }
  }
}
