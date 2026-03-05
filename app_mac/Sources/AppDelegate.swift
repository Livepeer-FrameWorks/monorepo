import Cocoa
import ServiceManagement
import SwiftUI

class AppDelegate: NSObject, NSApplicationDelegate {
  var statusItem: NSStatusItem!
  let appState = AppState()
  private var panelManager: PanelManager!
  private var edgePoller: EdgePoller?

  func applicationDidFinishLaunching(_ notification: Notification) {
    panelManager = PanelManager(appState: appState)
    setupStatusBarIcon()
    tryRestoreSession()
    startEdgeDetection()
    registerLoginItem()
  }

  private func registerLoginItem() {
    if #available(macOS 13.0, *) {
      let service = SMAppService.mainApp
      if service.status != .enabled {
        try? service.register()
      }
    }
  }

  // MARK: - Status Bar

  private func setupStatusBarIcon() {
    statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    if let button = statusItem.button {
      if let originalImage = NSImage(named: "StatusIcon") {
        let targetSize = NSSize(width: 16, height: 16)
        let resizedImage = NSImage(size: targetSize)

        resizedImage.lockFocus()
        let originalSize = originalImage.size
        let aspectRatio = originalSize.width / originalSize.height

        var drawSize = targetSize
        if aspectRatio > 1 {
          drawSize.height = targetSize.width / aspectRatio
        } else {
          drawSize.width = targetSize.height * aspectRatio
        }

        let drawRect = NSRect(
          x: (targetSize.width - drawSize.width) / 2,
          y: (targetSize.height - drawSize.height) / 2,
          width: drawSize.width,
          height: drawSize.height
        )

        originalImage.draw(in: drawRect)
        resizedImage.unlockFocus()
        resizedImage.isTemplate = true
        button.image = resizedImage
      }
      button.toolTip = "FrameWorks"
      button.target = self
      button.action = #selector(handleStatusBarClick(_:))
      button.sendAction(on: [.leftMouseUp, .rightMouseUp])
    }
  }

  @objc private func handleStatusBarClick(_ sender: NSStatusBarButton) {
    guard let event = NSApp.currentEvent else { return }
    if event.type == .rightMouseUp {
      showContextMenu()
    } else {
      panelManager.togglePanel(relativeTo: sender)
    }
  }

  private func showContextMenu() {
    let menu = NSMenu()

    if appState.isAuthenticated {
      let userItem = NSMenuItem(title: appState.userEmail ?? "Logged in", action: nil, keyEquivalent: "")
      userItem.isEnabled = false
      menu.addItem(userItem)
      menu.addItem(NSMenuItem.separator())

      if appState.edgeDetected {
        let edgeItem = NSMenuItem(
          title: appState.edgeHealthy ? "Edge: Healthy" : "Edge: Unhealthy",
          action: nil, keyEquivalent: "")
        edgeItem.image = SFSymbols.tintedImage(
          "circle.fill",
          color: appState.edgeHealthy ? .systemGreen : .systemRed)
        menu.addItem(edgeItem)

        let streamsItem = NSMenuItem(
          title: "\(appState.activeStreamCount) streams, \(appState.totalViewers) viewers",
          action: nil, keyEquivalent: "")
        menu.addItem(streamsItem)
        menu.addItem(NSMenuItem.separator())
      }

      menu.addItem(NSMenuItem(title: "Dashboard", action: #selector(openDashboard), keyEquivalent: "d"))
      menu.addItem(NSMenuItem(title: "Ask Skipper...", action: #selector(openSkipper), keyEquivalent: "s"))
      menu.addItem(NSMenuItem.separator())
      menu.addItem(NSMenuItem(title: "Settings", action: #selector(openSettings), keyEquivalent: ","))
      menu.addItem(NSMenuItem(title: "Log Out", action: #selector(logout), keyEquivalent: ""))
    } else {
      menu.addItem(NSMenuItem(title: "Log In...", action: #selector(openLogin), keyEquivalent: "l"))
    }

    menu.addItem(NSMenuItem.separator())
    menu.addItem(NSMenuItem(title: "Quit FrameWorks", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q"))

    statusItem.menu = menu
    statusItem.button?.performClick(nil)
    statusItem.menu = nil
  }

  // MARK: - Actions

  @objc private func openDashboard() {
    guard let button = statusItem.button else { return }
    panelManager.showView(.dashboard, relativeTo: button)
  }

  @objc private func openSkipper() {
    guard let button = statusItem.button else { return }
    panelManager.showView(.skipper, relativeTo: button)
  }

  @objc private func openSettings() {
    guard let button = statusItem.button else { return }
    panelManager.showView(.settings, relativeTo: button)
  }

  @objc private func openLogin() {
    guard let button = statusItem.button else { return }
    panelManager.showView(.login, relativeTo: button)
  }

  @objc private func logout() {
    Task {
      await AuthService.shared.logout(appState: appState)
      await MainActor.run {
        edgePoller?.stop()
        edgePoller = nil
        updateStatusIcon()
      }
    }
  }

  // MARK: - Session Restore

  private func tryRestoreSession() {
    Task {
      let restored = await AuthService.shared.restoreSession(appState: appState)
      await MainActor.run {
        if restored {
          startEdgePolling()
        }
        updateStatusIcon()
      }
    }
  }

  // MARK: - Edge Detection

  private func startEdgeDetection() {
    Task {
      let reachable = await EdgeClient.shared.isReachable()
      let domain = ServiceManager.detectedDomain()
      await MainActor.run {
        appState.edgeDetected = reachable
        appState.edgeServiceDomain = domain
        if reachable && appState.isAuthenticated {
          startEdgePolling()
        }
        updateStatusIcon()
      }
    }
  }

  private func startEdgePolling() {
    guard edgePoller == nil else { return }
    edgePoller = EdgePoller(appState: appState)
    edgePoller?.start()
  }

  // MARK: - Status Icon

  func updateStatusIcon() {
    // Icon is set once in setupStatusBarIcon; no dynamic changes needed
  }
}
