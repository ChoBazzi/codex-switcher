import Foundation
import SwitcherUIModel

/// Private loopback metadata only. Never follows redirects or sends account tokens.
final class SessionBridge: NSObject, URLSessionTaskDelegate {
    private let token: String
    private let endpoint: URL
    private lazy var session: URLSession = {
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 2
        config.timeoutIntervalForResource = 2
        config.urlCache = nil
        config.httpCookieStorage = nil
        config.connectionProxyDictionary = [:]
        return URLSession(configuration: config, delegate: self, delegateQueue: nil)
    }()

    init?(environment: [String: String]) {
        guard let token = environment["SWITCHER_CONTROL_TOKEN"], token.count >= 32,
              !token.contains("\n"), !token.contains("\r"),
              let url = URL(string: environment["SWITCHER_CONTROL_URL"] ?? "http://127.0.0.1:8766"),
              url.scheme == "http", url.host == "127.0.0.1", url.user == nil, url.password == nil,
              url.query == nil, url.fragment == nil, url.path.isEmpty || url.path == "/" else { return nil }
        self.token = token
        self.endpoint = url.appendingPathComponent("control/sessions")
    }

    func read() async throws -> SessionSnapshot {
        var request = URLRequest(url: endpoint)
        request.setValue("Bearer " + token, forHTTPHeaderField: "Authorization")
        request.cachePolicy = .reloadIgnoringLocalCacheData
        let (bytes, response) = try await session.bytes(for: request)
        guard let http = response as? HTTPURLResponse, http.statusCode == 200,
              http.mimeType == "application/json" else { throw SnapshotError.invalid }
        var data = Data()
        for try await byte in bytes {
            guard data.count < 128 * 1024 else { throw SnapshotError.invalid }
            data.append(byte)
        }
        let snapshot = try SessionSnapshot.decode(data)
        guard abs(snapshot.generatedAt.timeIntervalSinceNow) < 5 else { throw SnapshotError.invalid }
        return snapshot
    }

    func close() { session.invalidateAndCancel() }

    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest,
                    completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil)
    }
}
