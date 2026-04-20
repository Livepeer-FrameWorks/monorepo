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
    registerLoginItem()
    bootstrap()
    MCPServer.shared.start()
  }

  // bootstrap runs the dependent startup steps in order: detect the CLI and
  // hydrate the active context (so GatewayClient.baseURL is populated) before
  // any code that issues a Gateway request. Without this, restoreSession would
  // fire against an empty baseURL and the tray would always splash logged-out.
  private func bootstrap() {
    Task {
      await detectAndLoadCLIContext()
      let restored = await AuthService.shared.restoreSession(appState: appState)
      await MainActor.run {
        if restored {
          startEdgePolling()
        }
        updateStatusIcon()
      }
      await runEdgeDetection()
    }
  }

  func applicationWillTerminate(_ notification: Notification) {
    MCPServer.shared.stop()
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

      if appState.cliAvailable {
        menu.addItem(NSMenuItem.separator())
        menu.addItem(NSMenuItem(title: "Diagnostics...", action: #selector(openDiagnostics), keyEquivalent: ""))
        menu.addItem(NSMenuItem(title: "Provision Edge...", action: #selector(openProvision), keyEquivalent: ""))

        let contextSubmenu = NSMenu()
        for name in appState.availableContexts {
          let item = NSMenuItem(title: name, action: #selector(switchContextFromMenu(_:)), keyEquivalent: "")
          item.target = self
          item.representedObject = name
          if name == appState.currentContext {
            item.state = .on
          }
          contextSubmenu.addItem(item)
        }
        if !appState.availableContexts.isEmpty {
          contextSubmenu.addItem(NSMenuItem.separator())
        }
        contextSubmenu.addItem(NSMenuItem(title: "Manage...", action: #selector(openContextPicker), keyEquivalent: ""))
        let contextItem = NSMenuItem(title: "Context", action: nil, keyEquivalent: "")
        contextItem.submenu = contextSubmenu
        menu.addItem(contextItem)
      }

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

  @objc private func openDiagnostics() {
    guard let button = statusItem.button else { return }
    panelManager.showView(.diagnostics, relativeTo: button)
  }

  @objc private func openContextPicker() {
    guard let button = statusItem.button else { return }
    panelManager.showView(.contextPicker, relativeTo: button)
  }

  @objc private func openProvision() {
    guard let button = statusItem.button else { return }
    panelManager.showView(.provision, relativeTo: button)
  }

  @objc private func switchContextFromMenu(_ sender: NSMenuItem) {
    guard let name = sender.representedObject as? String else { return }
    Task {
      let ok = await ConfigBridge.shared.switchContext(name)
      if ok {
        reloadCLIContext()
      }
    }
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

  // MARK: - Edge Detection

  private func runEdgeDetection() async {
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

  private func startEdgePolling() {
    guard edgePoller == nil else { return }
    edgePoller = EdgePoller(appState: appState)
    edgePoller?.start()
  }

  // MARK: - CLI Detection

  // detectAndLoadCLIContext finds the CLI binary, loads context state, and
  // hydrates GatewayClient.baseURL via applyHydratedContext. It must complete
  // before any code that makes a Gateway request — there is no public default
  // to fall back on.
  private func detectAndLoadCLIContext() async {
    let available = await CLIRunner.shared.isAvailable()
    var version: String?
    var current: CLIContext?
    var contextNames: [String] = []

    if available {
      version = await CLIRunner.shared.version()
      let contexts = await ConfigBridge.shared.loadContexts()
      contextNames = contexts.map(\.name)
      current = await ConfigBridge.shared.loadCurrentContext()
      await ConfigBridge.shared.startWatching { [weak self] in
        self?.reloadCLIContext()
      }
    }

    await MainActor.run {
      appState.cliAvailable = available
      appState.cliVersion = version
    }
    await applyHydratedContext(current, contextNames: contextNames, retryRestore: false)
  }

  // applyHydratedContext is the single entry point for "the active CLI context
  // changed." Used by initial bootstrap, the file-watcher reload, and the
  // tray's context picker. Handles three scenarios:
  //   - context present, Bridge URL same as before  → idempotent refresh
  //   - context present, Bridge URL changed         → re-hydrate + retry session
  //   - context absent (nil)                        → clear to no-context state
  func applyHydratedContext(
    _ ctx: CLIContext?,
    contextNames: [String],
    retryRestore: Bool = true
  ) async {
    guard let ctx else {
      await clearToNoContextState(contextNames: contextNames)
      return
    }

    let newURL = ctx.endpoints.bridgeURL.trimmingCharacters(in: .whitespacesAndNewlines)
    let previousURL = await MainActor.run { () -> String in
      let prev = appState.gatewayBaseURL.trimmingCharacters(in: .whitespacesAndNewlines)
      appState.currentContext = ctx.name
      appState.gatewayBaseURL = ctx.endpoints.bridgeURL
      GatewayClient.shared.baseURL = ctx.endpoints.bridgeURL
      appState.availableContexts = contextNames
      return prev
    }

    guard retryRestore, !newURL.isEmpty, newURL != previousURL else { return }

    // Bridge URL changed (or just became known). The previous Gateway session
    // was attached to the old bridge — drop it locally so the user sees a
    // consistent state, then attempt restore against the new bridge.
    await clearAuthState()
    let restored = await AuthService.shared.restoreSession(appState: appState)
    await MainActor.run {
      if restored {
        startEdgePolling()
      } else {
        edgePoller?.stop()
        edgePoller = nil
      }
      updateStatusIcon()
    }
  }

  // clearToNoContextState mirrors a full no-context reset: empty Bridge URL,
  // empty active context, auth dropped (no network call — loss of CLI context
  // is not a server logout), no edge polling. The picker, the watcher, and
  // any future caller all converge on this state.
  private func clearToNoContextState(contextNames: [String]) async {
    await clearAuthState()
    await MainActor.run {
      appState.currentContext = ""
      appState.gatewayBaseURL = ""
      GatewayClient.shared.baseURL = ""
      appState.availableContexts = contextNames
      edgePoller?.stop()
      edgePoller = nil
      updateStatusIcon()
    }
  }

  // clearAuthState mirrors AuthService.logout's local cleanup but skips the
  // /auth/logout network call — the trigger is "we lost the bridge," not
  // "the user pressed log out."
  private func clearAuthState() async {
    KeychainHelper.delete(key: "user_session")
    KeychainHelper.delete(key: "refresh_token")
    await MainActor.run {
      appState.isAuthenticated = false
      appState.userEmail = nil
      appState.userId = nil
      appState.tenantId = nil
      appState.streams = []
    }
  }

  private func reloadCLIContext() {
    Task {
      let ctx = await ConfigBridge.shared.loadCurrentContext()
      let contexts = await ConfigBridge.shared.loadContexts()
      await applyHydratedContext(ctx, contextNames: contexts.map(\.name))
    }
  }

  // MARK: - Status Icon

  func updateStatusIcon() {
    // Icon is set once in setupStatusBarIcon; no dynamic changes needed
  }
}
