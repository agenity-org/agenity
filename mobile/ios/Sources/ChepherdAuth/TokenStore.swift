// Keychain-backed token cache.
// On iOS the Security framework provides the Keychain API; on macOS
// the same kSecClassGenericPassword item class works with appropriate
// access groups.

#if canImport(Security)
import Foundation
import Security

public actor TokenStore {
    private let service: String

    public init(service: String = "io.chepherd.rc.tokens") {
        self.service = service
    }

    public func save(_ tokens: TokenSet) throws {
        let data = try JSONEncoder().encode(tokens)
        // Remove any existing entry first.
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: "chepherd-tokens",
        ]
        SecItemDelete(query as CFDictionary)
        var add = query
        add[kSecValueData as String] = data
        add[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlockThisDeviceOnly
        let status = SecItemAdd(add as CFDictionary, nil)
        guard status == errSecSuccess else {
            throw NSError(domain: "TokenStore", code: Int(status), userInfo: [
                NSLocalizedDescriptionKey: "keychain add failed status=\(status)",
            ])
        }
    }

    public func load() -> TokenSet? {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: "chepherd-tokens",
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne,
        ]
        var raw: AnyObject?
        let status = SecItemCopyMatching(query as CFDictionary, &raw)
        guard status == errSecSuccess, let data = raw as? Data else { return nil }
        return try? JSONDecoder().decode(TokenSet.self, from: data)
    }

    public func clear() {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: "chepherd-tokens",
        ]
        SecItemDelete(query as CFDictionary)
    }
}
#endif
