// Spacing scale — §4 of the design system. Single doubling progression.

import Foundation

public enum ChepherdSpace {
    public static let s0:  CGFloat = 0
    public static let s1:  CGFloat = 4
    public static let s2:  CGFloat = 8
    public static let s3:  CGFloat = 12
    public static let s4:  CGFloat = 16
    public static let s6:  CGFloat = 24
    public static let s8:  CGFloat = 32
    public static let s12: CGFloat = 48
}

#if canImport(CoreGraphics)
import CoreGraphics
#endif
