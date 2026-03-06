import Cocoa
import SwiftUI

enum PanelView {
  case dashboard
  case login
  case settings
  case skipper
  case edgeStatus
  case diagnostics
  case contextPicker
  case provision
}

private class KeyablePanel: NSPanel {
  override var canBecomeKey: Bool { true }

  override func sendEvent(_ event: NSEvent) {
    if event.type == .leftMouseDown && !isKeyWindow {
      makeKeyAndOrderFront(nil)
    }
    super.sendEvent(event)
  }
}

class PanelManager: NSObject, NSWindowDelegate {
  private var panel: NSPanel?
  private let appState: AppState
  private let panelWidth: CGFloat = 420
  private let panelHeight: CGFloat = 560
  private(set) weak var statusBarButton: NSStatusBarButton?

  init(appState: AppState) {
    self.appState = appState
    super.init()
  }

  func togglePanel(relativeTo button: NSStatusBarButton) {
    if let panel = panel, panel.isVisible {
      closePanel()
    } else {
      let view: PanelView = appState.isAuthenticated ? .dashboard : .login
      showView(view, relativeTo: button)
    }
  }

  func showView(_ view: PanelView, relativeTo button: NSStatusBarButton) {
    statusBarButton = button
    closePanel()

    let panel = makePanel()
    let hostingView = NSHostingController(rootView: contentView(for: view))
    panel.contentViewController = hostingView
    panel.setContentSize(NSSize(width: panelWidth, height: panelHeight))

    if let buttonWindow = button.window {
      let buttonFrame = buttonWindow.frame
      let x = buttonFrame.midX - (panelWidth / 2)
      let y = buttonFrame.minY - panelHeight - 4
      panel.setFrameOrigin(NSPoint(x: x, y: y))
    } else {
      panel.center()
    }

    self.panel = panel
    NSApp.activate(ignoringOtherApps: true)
    panel.makeKeyAndOrderFront(nil)
  }

  func closePanel() {
    panel?.orderOut(nil)
    panel = nil
  }

  @ViewBuilder
  private func contentView(for view: PanelView) -> some View {
    switch view {
    case .dashboard:
      DashboardView(appState: appState, closePanel: { [weak self] in self?.closePanel() })
    case .login:
      LoginView(appState: appState, closePanel: { [weak self] in self?.closePanel() })
    case .settings:
      SettingsView(appState: appState, closePanel: { [weak self] in self?.closePanel() })
    case .skipper:
      SkipperChatView(appState: appState, closePanel: { [weak self] in self?.closePanel() })
    case .edgeStatus:
      EdgeStatusView(appState: appState, closePanel: { [weak self] in self?.closePanel() }) { [weak self] in
        guard let self, let button = self.statusBarButton else { return }
        self.showView(.diagnostics, relativeTo: button)
      }
    case .diagnostics:
      DiagnosticsView(appState: appState, closePanel: { [weak self] in self?.closePanel() })
    case .contextPicker:
      ContextPickerView(appState: appState, closePanel: { [weak self] in self?.closePanel() })
    case .provision:
      ProvisionView(appState: appState, closePanel: { [weak self] in self?.closePanel() })
    }
  }

  private func makePanel() -> NSPanel {
    let panel = KeyablePanel(
      contentRect: NSRect(x: 0, y: 0, width: panelWidth, height: panelHeight),
      styleMask: [.borderless, .nonactivatingPanel],
      backing: .buffered,
      defer: false
    )
    panel.isFloatingPanel = true
    panel.level = .floating
    panel.hasShadow = true
    panel.isOpaque = false
    panel.backgroundColor = .clear
    panel.delegate = self
    panel.isMovableByWindowBackground = false
    panel.hidesOnDeactivate = false
    return panel
  }

  func windowDidResignKey(_ notification: Notification) {
    closePanel()
  }
}
