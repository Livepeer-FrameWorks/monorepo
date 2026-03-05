import SwiftUI

struct SkipperChatView: View {
  @ObservedObject var appState: AppState
  var closePanel: () -> Void

  @State private var input = ""
  @State private var isLoading = false

  var body: some View {
    VStack(spacing: 0) {
      HStack {
        Image(systemName: "bubble.left.and.text.bubble.right")
          .foregroundStyle(Color.tnPurple)
        Text("Skipper").font(.title2.bold())
        Spacer()
        Button(action: closePanel) {
          Image(systemName: "xmark.circle.fill").foregroundStyle(.secondary)
        }
        .buttonStyle(.plain)
      }
      .padding()

      Divider()

      // Messages
      ScrollViewReader { proxy in
        ScrollView {
          LazyVStack(alignment: .leading, spacing: 12) {
            ForEach(appState.skipperMessages) { message in
              messageRow(message)
                .id(message.id)
            }
            if isLoading {
              HStack(spacing: 8) {
                ProgressView().controlSize(.small)
                Text("Thinking...").font(.caption).foregroundStyle(.secondary)
              }
              .padding(.horizontal)
            }
          }
          .padding()
        }
        .onChange(of: appState.skipperMessages.count) { _ in
          if let last = appState.skipperMessages.last {
            proxy.scrollTo(last.id, anchor: .bottom)
          }
        }
      }

      Divider()

      // Input
      HStack(spacing: 8) {
        TextField("Ask anything...", text: $input)
          .textFieldStyle(.roundedBorder)
          .onSubmit { sendMessage() }

        Button(action: sendMessage) {
          Image(systemName: "arrow.up.circle.fill")
            .font(.title2)
            .foregroundStyle(input.isEmpty ? Color.secondary : Color.tnAccent)
        }
        .buttonStyle(.plain)
        .disabled(input.isEmpty || isLoading)
      }
      .padding()
    }
    .frame(width: 420, height: 560)
    .background(.regularMaterial)
  }

  private func messageRow(_ message: SkipperMessage) -> some View {
    let isUser = message.role == "user"
    return HStack(alignment: .top, spacing: 8) {
      if !isUser {
        Image(systemName: "sparkles")
          .font(.caption)
          .foregroundStyle(Color.tnPurple)
      }
      Text(message.content)
        .font(.body)
        .textSelection(.enabled)
        .foregroundStyle(isUser ? .primary : .primary)
      if isUser { Spacer() }
    }
    .padding(.horizontal, isUser ? 16 : 0)
    .padding(.vertical, isUser ? 8 : 4)
    .background(isUser ? Color.tnAccent.opacity(0.1) : Color.clear)
    .clipShape(RoundedRectangle(cornerRadius: 8))
  }

  private func sendMessage() {
    let text = input.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !text.isEmpty else { return }

    appState.skipperMessages.append(SkipperMessage(role: "user", content: text))
    input = ""
    isLoading = true

    Task {
      do {
        let response = try await SkipperService.shared.ask(question: text)
        await MainActor.run {
          appState.skipperMessages.append(SkipperMessage(role: "assistant", content: response))
          isLoading = false
        }
      } catch {
        await MainActor.run {
          appState.skipperMessages.append(
            SkipperMessage(role: "assistant", content: "Error: \(error.localizedDescription)"))
          isLoading = false
        }
      }
    }
  }
}
