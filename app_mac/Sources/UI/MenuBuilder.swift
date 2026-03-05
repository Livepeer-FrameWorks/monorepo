import Cocoa

enum SFSymbols {
  static func image(
    _ name: String, accessibilityDescription: String? = nil
  ) -> NSImage? {
    guard let image = NSImage(
      systemSymbolName: name, accessibilityDescription: accessibilityDescription)
    else { return nil }
    let config = NSImage.SymbolConfiguration(pointSize: 13, weight: .regular)
    return image.withSymbolConfiguration(config)
  }

  static func tintedImage(
    _ name: String, color: NSColor, accessibilityDescription: String? = nil
  ) -> NSImage? {
    guard let image = NSImage(
      systemSymbolName: name, accessibilityDescription: accessibilityDescription)
    else { return nil }
    let config = NSImage.SymbolConfiguration(paletteColors: [color])
      .applying(NSImage.SymbolConfiguration(pointSize: 13, weight: .regular))
    return image.withSymbolConfiguration(config)
  }
}
