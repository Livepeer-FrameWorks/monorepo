import SwiftUI

struct StreamListView: View {
  @ObservedObject var appState: AppState

  var body: some View {
    VStack(alignment: .leading, spacing: 8) {
      ForEach(appState.streams) { stream in
        HStack {
          Circle()
            .fill(stream.ingestState == nil ? Color.tnYellow : (stream.isActive ? Color.tnGreen : Color.secondary.opacity(0.3)))
            .frame(width: 8, height: 8)
          VStack(alignment: .leading, spacing: 2) {
            Text(stream.name).font(.body)
            Text(stream.id).font(.caption2).foregroundStyle(.tertiary)
          }
          Spacer()
          if stream.isActive, let viewerCount = stream.viewerCount {
            HStack(spacing: 4) {
              Image(systemName: "person.2").font(.caption2)
              Text("\(viewerCount)")
            }
            .font(.caption)
            .foregroundStyle(Color.tnGreen)
            .padding(.horizontal, 8)
            .padding(.vertical, 2)
            .background(Color.tnGreen.opacity(0.1))
            .clipShape(Capsule())
          } else {
            Text(stream.liveStateLabel)
              .font(.caption)
              .foregroundStyle(.tertiary)
          }
        }
        .padding(.vertical, 4)
      }
    }
  }
}
