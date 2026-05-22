import SwiftUI

struct DiagnosticsView: View {
  @ObservedObject var appState: AppState
  var closePanel: () -> Void

  @State private var catalog: CLIMenuCatalog?
  @State private var commandCatalog: CLICommandCatalog?
  @State private var selectedActionKey = ""
  @State private var selectedCatalogCommand: CLICommandEntry?
  @State private var loadingCatalog = false
  @State private var confirmingActionKey: String?
  @State private var commandSearch = ""
  @State private var showAllCommands = false
  @State private var showOptionalCommandInputs = false
  @State private var commandArgValues: [String: String] = [:]
  @State private var commandFlagValues: [String: String] = [:]
  @State private var commandBoolValues: [String: Bool] = [:]

  private var sections: [CLIMenuSection] {
    catalog?.sections ?? fallbackCatalog.sections
  }

  private var selectedAction: CLIMenuAction? {
    if let selectedCatalogCommand {
      return action(for: selectedCatalogCommand)
    }
    for section in sections {
      if let action = section.actions.first(where: { $0.key == selectedActionKey }) {
        return action
      }
    }
    return nil
  }

  private var selectedActionCanRun: Bool {
    guard let action = selectedAction else { return false }
    if action.interactive {
      return false
    }
    if let command = selectedCatalogCommand {
      return missingRequiredInputs(for: command).isEmpty
    }
    return true
  }

  private var commandResults: [CLICommandEntry] {
    let commands = (commandCatalog?.commands ?? [])
      .filter { $0.runnable && ($0.hidden != true) && $0.path.count > 1 }
    let query = commandSearch.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
    let filtered: [CLICommandEntry]
    if query.isEmpty {
      filtered = commands
    } else {
      filtered = commands.filter { command in
        command.command.lowercased().contains(query)
          || command.use.lowercased().contains(query)
          || (command.short ?? "").lowercased().contains(query)
      }
    }
    return Array(filtered.prefix(8))
  }

  private var fallbackCatalog: CLIMenuCatalog {
    let actions = [
      CLIMenuAction(
        key: "context-check",
        label: "Context Check",
        description: "Check reachability and persona/auth invariants.",
        args: ["context", "check"],
        longRunning: false,
        risk: nil,
        interactive: false),
      CLIMenuAction(
        key: "cli-update-check",
        label: "Check CLI Update",
        description: "Check whether a CLI update is available.",
        args: ["update", "--check"],
        longRunning: true,
        risk: nil,
        interactive: false),
    ]
    return CLIMenuCatalog(
      persona: appState.currentPersona,
      sections: [
        CLIMenuSection(
          key: "settings",
          label: "Settings & Contexts",
          recommended: true,
          actions: actions)
      ])
  }

  var body: some View {
    VStack(spacing: 0) {
      header
      Divider()
      ScrollView {
        controls
      }
      .frame(maxHeight: 300)
      Divider()
      outputView
      footer
    }
    .frame(width: 420, height: 560)
    .background(.regularMaterial)
    .tint(Color.tnAccent)
    .onAppear(perform: loadCatalog)
    .onChange(of: appState.currentContext) { _, _ in loadCatalog() }
    .onChange(of: appState.currentPersona) { _, _ in loadCatalog() }
  }

  private var header: some View {
    HStack {
      Image(systemName: "terminal").foregroundStyle(Color.tnAccent)
      Text("Command Center").font(.title2.bold())
      Spacer()
      Button(action: closePanel) {
        Image(systemName: "xmark.circle.fill").foregroundStyle(.secondary)
      }
      .buttonStyle(.plain)
    }
    .padding()
  }

  private var controls: some View {
    VStack(alignment: .leading, spacing: 10) {
      HStack {
        Picker("Action", selection: $selectedActionKey) {
          ForEach(sections) { section in
            Section(section.label) {
              ForEach(section.actions) { action in
                Text(action.label).tag(action.key)
              }
            }
          }
        }
        .pickerStyle(.menu)
        .disabled(loadingCatalog || appState.isDiagnosticRunning)
        .onChange(of: selectedActionKey) { _, _ in
          selectedCatalogCommand = nil
          confirmingActionKey = nil
        }

        if loadingCatalog {
          ProgressView().controlSize(.small)
        } else if appState.isDiagnosticRunning {
          ProgressView().controlSize(.small)
        } else {
          Button(action: runSelectedAction) {
            Image(systemName: confirmingActionKey == selectedAction?.key ? "exclamationmark.triangle.fill" : "play.fill")
          }
          .buttonStyle(.bordered)
          .controlSize(.small)
          .disabled(!selectedActionCanRun)
        }
      }

      if let action = selectedAction {
        VStack(alignment: .leading, spacing: 6) {
          Text(action.commandText)
            .font(.system(.caption, design: .monospaced))
            .textSelection(.enabled)
          if let description = action.description, !description.isEmpty {
            Text(description)
              .font(.caption2)
              .foregroundStyle(.secondary)
          }
          HStack(spacing: 6) {
            if action.longRunning {
              tag("long-running", color: .tnCyan)
            }
            if let risk = action.risk, !risk.isEmpty {
              tag(risk, color: .tnOrange)
            }
            if action.interactive {
              tag("interactive CLI", color: .tnPurple)
            }
          }
          if let command = selectedCatalogCommand {
            commandInputForm(command)
          }
        }
      }

      DisclosureGroup("All CLI Commands", isExpanded: $showAllCommands) {
        VStack(alignment: .leading, spacing: 8) {
          TextField("Search commands", text: $commandSearch)
            .textFieldStyle(.roundedBorder)
            .disabled(loadingCatalog || appState.isDiagnosticRunning)

          ForEach(commandResults) { command in
            Button(action: { selectCommand(command) }) {
              HStack {
                VStack(alignment: .leading, spacing: 2) {
                  Text(command.command)
                    .font(.system(.caption, design: .monospaced))
                  if let short = command.short, !short.isEmpty {
                    Text(short)
                      .font(.caption2)
                      .foregroundStyle(.secondary)
                      .lineLimit(1)
                  }
                }
                Spacer()
                if commandHasInputs(command) {
                  Image(systemName: "ellipsis.rectangle")
                    .foregroundStyle(Color.tnOrange)
                } else if let risk = command.risk, !risk.isEmpty {
                  Image(systemName: "exclamationmark.triangle")
                    .foregroundStyle(Color.tnOrange)
                }
              }
            }
            .buttonStyle(.plain)
            .disabled(appState.isDiagnosticRunning)
          }

          if commandResults.isEmpty {
            Text("No matching commands")
              .font(.caption2)
              .foregroundStyle(.secondary)
          }
        }
        .padding(.top, 6)
      }
    }
    .padding(.horizontal)
    .padding(.vertical, 10)
  }

  private var outputView: some View {
    ScrollViewReader { proxy in
      ScrollView {
        Text(appState.diagnosticOutput.isEmpty ? "Pick a CLI action and press run." : appState.diagnosticOutput)
          .font(.system(.caption, design: .monospaced))
          .frame(maxWidth: .infinity, alignment: .leading)
          .padding()
          .id("output")
      }
      .onChange(of: appState.diagnosticOutput) { _, _ in
        proxy.scrollTo("output", anchor: .bottom)
      }
    }
  }

  @ViewBuilder
  private var footer: some View {
    if !appState.diagnosticOutput.isEmpty {
      Divider()
      HStack {
        Button("Copy") {
          NSPasteboard.general.clearContents()
          NSPasteboard.general.setString(appState.diagnosticOutput, forType: .string)
        }
        .buttonStyle(.bordered)
        .controlSize(.small)

        Button("Clear") {
          appState.diagnosticOutput = ""
          confirmingActionKey = nil
        }
        .buttonStyle(.bordered)
        .controlSize(.small)

        Spacer()
      }
      .padding(8)
    }
  }

  private func tag(_ label: String, color: Color) -> some View {
    Text(label)
      .font(.caption2.bold())
      .padding(.horizontal, 6)
      .padding(.vertical, 2)
      .background(color.opacity(0.15))
      .foregroundStyle(color)
      .clipShape(Capsule())
  }

  @ViewBuilder
  private func commandInputForm(_ command: CLICommandEntry) -> some View {
    let arguments = command.arguments ?? []
    let flags = commandFormFlags(command)
    let requiredFlags = flags.filter { $0.required == true }
    let optionalFlags = flags.filter { $0.required != true }

    if !arguments.isEmpty || !flags.isEmpty {
      VStack(alignment: .leading, spacing: 8) {
        ForEach(arguments) { argument in
          commandArgumentField(argument)
        }
        ForEach(requiredFlags) { flag in
          commandFlagField(flag)
        }
        if !optionalFlags.isEmpty {
          DisclosureGroup("Optional flags", isExpanded: $showOptionalCommandInputs) {
            VStack(alignment: .leading, spacing: 8) {
              ForEach(optionalFlags) { flag in
                commandFlagField(flag)
              }
            }
            .padding(.top, 6)
          }
          .font(.caption)
        }
      }
      .padding(.top, 4)
    }
  }

  @ViewBuilder
  private func commandArgumentField(_ argument: CLICommandArgument) -> some View {
    VStack(alignment: .leading, spacing: 3) {
      Text(argument.required == true ? "\(argument.name) *" : argument.name)
        .font(.caption2)
        .foregroundStyle(.secondary)
      TextField(argument.raw, text: argumentBinding(argument))
        .textFieldStyle(.roundedBorder)
        .font(.caption)
    }
  }

  @ViewBuilder
  private func commandFlagField(_ flag: CLICommandFlag) -> some View {
    if flag.type == "bool" {
      Toggle(commandFlagLabel(flag), isOn: boolBinding(flag))
        .toggleStyle(.checkbox)
        .font(.caption)
    } else {
      VStack(alignment: .leading, spacing: 3) {
        Text(commandFlagLabel(flag))
          .font(.caption2)
          .foregroundStyle(.secondary)
        if flag.sensitive == true {
          SecureField(commandFlagPlaceholder(flag), text: flagBinding(flag))
            .textFieldStyle(.roundedBorder)
            .font(.caption)
        } else {
          TextField(commandFlagPlaceholder(flag), text: flagBinding(flag))
            .textFieldStyle(.roundedBorder)
            .font(.caption)
        }
        if let usage = flag.usage, !usage.isEmpty {
          Text(usage)
            .font(.caption2)
            .foregroundStyle(.secondary)
            .lineLimit(2)
        }
      }
    }
  }

  private func loadCatalog() {
    guard appState.cliAvailable else {
      catalog = nil
      commandCatalog = nil
      ensureSelectedActionAvailable()
      return
    }

    loadingCatalog = true
    Task {
      async let loadedMenu = ConfigBridge.shared.loadMenuCatalog()
      async let loadedCommands = ConfigBridge.shared.loadCommandCatalog()
      let loaded = await loadedMenu
      let commands = await loadedCommands
      await MainActor.run {
        catalog = loaded
        commandCatalog = commands
        loadingCatalog = false
        ensureSelectedActionAvailable()
      }
    }
  }

  private func ensureSelectedActionAvailable() {
    let allActions = sections.flatMap(\.actions)
    if !allActions.contains(where: { $0.key == selectedActionKey }) {
      selectedActionKey = allActions.first?.key ?? ""
      confirmingActionKey = nil
      selectedCatalogCommand = nil
    }
  }

  private func selectCommand(_ command: CLICommandEntry) {
    selectedCatalogCommand = command
    commandArgValues = [:]
    commandFlagValues = [:]
    commandBoolValues = defaultBoolValues(for: command)
    showOptionalCommandInputs = false
    confirmingActionKey = nil
  }

  private func action(for command: CLICommandEntry) -> CLIMenuAction {
    var args = Array(command.path.dropFirst())
    for argument in command.arguments ?? [] {
      let value = commandArgValues[argument.name]?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
      if !value.isEmpty {
        args.append(value)
      }
    }
    for flag in commandFormFlags(command) {
      if flag.type == "bool" {
        let defaultValue = flag.default == "true"
        let value = commandBoolValues[flag.name] ?? defaultValue
        if value != defaultValue {
          args.append(value ? "--\(flag.name)" : "--\(flag.name)=false")
        }
      } else {
        let value = commandFlagValues[flag.name]?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if !value.isEmpty {
          args.append("--\(flag.name)")
          args.append(value)
        }
      }
    }
    if command.risk != nil {
      for flag in commandConfirmationFlags(command) where flag.default != "true" {
        args.append("--\(flag.name)")
      }
    }

    let missing = missingRequiredInputs(for: command)
    return CLIMenuAction(
      key: "catalog:" + command.command,
      label: command.command,
      description: command.short,
      args: args,
      longRunning: command.risk == "mutating",
      risk: missing.isEmpty ? command.risk : "needs input",
      interactive: commandIsInteractive(command))
  }

  private func commandHasInputs(_ command: CLICommandEntry) -> Bool {
    !(command.arguments ?? []).isEmpty || !commandFormFlags(command).isEmpty
  }

  private func commandFormFlags(_ command: CLICommandEntry) -> [CLICommandFlag] {
    (command.flags ?? []).filter { flag in
      if flag.hidden == true || flag.deprecated != nil {
        return false
      }
      if flag.confirmation == true {
        return false
      }
      return flag.source != "frameworks"
    }
  }

  private func commandConfirmationFlags(_ command: CLICommandEntry) -> [CLICommandFlag] {
    (command.flags ?? []).filter { flag in
      flag.confirmation == true && flag.type == "bool" && flag.hidden != true && flag.deprecated == nil
    }
  }

  private func missingRequiredInputs(for command: CLICommandEntry) -> [String] {
    var missing: [String] = []
    for argument in command.arguments ?? [] where argument.required == true {
      let value = commandArgValues[argument.name]?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
      if value.isEmpty {
        missing.append(argument.name)
      }
    }
    for flag in commandFormFlags(command) where flag.required == true && flag.type != "bool" {
      let value = commandFlagValues[flag.name]?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
      if value.isEmpty {
        missing.append("--\(flag.name)")
      }
    }
    return missing
  }

  private func commandIsInteractive(_ command: CLICommandEntry) -> Bool {
    command.interactive == true
  }

  private func defaultBoolValues(for command: CLICommandEntry) -> [String: Bool] {
    var values: [String: Bool] = [:]
    for flag in commandFormFlags(command) where flag.type == "bool" {
      values[flag.name] = flag.default == "true"
    }
    return values
  }

  private func commandFlagLabel(_ flag: CLICommandFlag) -> String {
    flag.required == true ? "--\(flag.name) *" : "--\(flag.name)"
  }

  private func commandFlagPlaceholder(_ flag: CLICommandFlag) -> String {
    if let defaultValue = flag.default, !defaultValue.isEmpty {
      return defaultValue
    }
    return "--\(flag.name)"
  }

  private func argumentBinding(_ argument: CLICommandArgument) -> Binding<String> {
    Binding(
      get: { commandArgValues[argument.name] ?? "" },
      set: { commandArgValues[argument.name] = $0 }
    )
  }

  private func flagBinding(_ flag: CLICommandFlag) -> Binding<String> {
    Binding(
      get: { commandFlagValues[flag.name] ?? "" },
      set: { commandFlagValues[flag.name] = $0 }
    )
  }

  private func boolBinding(_ flag: CLICommandFlag) -> Binding<Bool> {
    Binding(
      get: { commandBoolValues[flag.name] ?? (flag.default == "true") },
      set: { commandBoolValues[flag.name] = $0 }
    )
  }

  private func runSelectedAction() {
    guard !appState.isDiagnosticRunning, let action = selectedAction else { return }
    if let command = selectedCatalogCommand {
      let missing = missingRequiredInputs(for: command)
      guard missing.isEmpty else {
        appState.diagnosticOutput = "[inputs required]\nMissing: \(missing.joined(separator: ", "))\n\(action.commandText)\n"
        return
      }
    }
    guard !action.interactive else {
      appState.diagnosticOutput = "[interactive CLI action]\nRun this in Terminal:\n\(action.commandText)\n"
      return
    }

    if let risk = action.risk, !risk.isEmpty, confirmingActionKey != action.key {
      confirmingActionKey = action.key
      appState.diagnosticOutput = "[confirm required]\nPress run again to execute:\n\(action.commandText)\n"
      return
    }

    confirmingActionKey = nil
    appState.diagnosticOutput = "$ \(action.commandText)\n"
    appState.isDiagnosticRunning = true

    Task {
      do {
        let exitCode = try await CLIRunner.shared.runStreaming(action.args) { line in
          Task { @MainActor in
            appState.diagnosticOutput += line + "\n"
          }
        }
        await MainActor.run {
          if exitCode != 0 {
            appState.diagnosticOutput += "\n[exited with code \(exitCode)]\n"
          }
          appState.isDiagnosticRunning = false
        }
      } catch {
        await MainActor.run {
          appState.diagnosticOutput += "\n[error: \(error.localizedDescription)]\n"
          appState.isDiagnosticRunning = false
        }
      }
    }
  }
}
