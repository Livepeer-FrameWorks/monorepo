import Cocoa
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
  }

  // MARK: - Status Bar

  private func setupStatusBarIcon() {
    statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    guard let button = statusItem.button else { return }

    button.image = SFSymbols.image("tv.badge.wifi", accessibilityDescription: "FrameWorks")
    button.sendAction(on: [.leftMouseUp, .rightMouseUp])
    button.action = #selector(handleStatusBarClick(_:))
    button.target = self
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
      await MainActor.run {
        appState.edgeDetected = reachable
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
    guard let button = statusItem.button else { return }

    if !appState.isAuthenticated {
      button.image = SFSymbols.image("tv.badge.wifi")
      return
    }

    if appState.edgeDetected {
      let color: NSColor = appState.edgeHealthy ? .systemGreen : .systemOrange
      button.image = SFSymbols.tintedImage("server.rack", color: color)
    } else {
      button.image = SFSymbols.tintedImage("tv.badge.wifi", color: .systemBlue)
    }
  }
}
