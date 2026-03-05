import SwiftUI

// Tokyo Night color palette
extension Color {
  /// Soft blue — primary interactive accent (#7aa2f7)
  static let tnAccent = Color(red: 0.478, green: 0.635, blue: 0.969)
  /// Subtle accent background
  static let tnAccentBg = Color(red: 0.478, green: 0.635, blue: 0.969).opacity(0.12)
  /// Green — online / success (#9ece6a)
  static let tnGreen = Color(red: 0.620, green: 0.808, blue: 0.416)
  /// Red-pink — error / destructive (#f7768e)
  static let tnRed = Color(red: 0.969, green: 0.463, blue: 0.557)
  /// Orange — caution / warning (#ff9e64)
  static let tnOrange = Color(red: 1.0, green: 0.620, blue: 0.392)
  /// Yellow — initializing / pending (#e0af68)
  static let tnYellow = Color(red: 0.878, green: 0.686, blue: 0.408)
  /// Purple — pushes / secondary accent (#bb9af7)
  static let tnPurple = Color(red: 0.733, green: 0.604, blue: 0.969)
  /// Cyan — info badges / protocols (#7dcfff)
  static let tnCyan = Color(red: 0.490, green: 0.812, blue: 1.0)
  /// Teal — alternate highlight (#73daca)
  static let tnTeal = Color(red: 0.451, green: 0.855, blue: 0.792)
}
