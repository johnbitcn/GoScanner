import CoreML
import Foundation

// Measure public API boundaries; MLModel loading can defer device work until prediction.
func seconds(_ start: UInt64) -> Double {
    Double(DispatchTime.now().uptimeNanoseconds - start) / 1_000_000_000
}

func run() throws {
    guard CommandLine.arguments.count == 3,
          ["compile", "load"].contains(CommandLine.arguments[1]) else {
        throw NSError(domain: "CoreMLTiming", code: 1,
                      userInfo: [NSLocalizedDescriptionKey: "usage: coreml_phases compile|load manifest.json"])
    }
    let mode = CommandLine.arguments[1]
    let data = try Data(contentsOf: URL(fileURLWithPath: CommandLine.arguments[2]))
    guard let models = try JSONSerialization.jsonObject(with: data) as? [[String: String]],
          !models.isEmpty else {
        throw NSError(domain: "CoreMLTiming", code: 2,
                      userInfo: [NSLocalizedDescriptionKey: "empty or invalid model manifest"])
    }
    var rows: [[String: Any]] = []
    for item in models {
        guard let source = item["source"], let destination = item["compiled"] else {
            throw NSError(domain: "CoreMLTiming", code: 3,
                          userInfo: [NSLocalizedDescriptionKey: "missing model path"])
        }
        let row: [String: Any] = try autoreleasepool {
            let target = URL(fileURLWithPath: destination, isDirectory: true)
            if mode == "compile" {
                guard !FileManager.default.fileExists(atPath: destination) else {
                    throw NSError(domain: "CoreMLTiming", code: 4,
                                  userInfo: [NSLocalizedDescriptionKey: "refusing to overwrite compiled model"])
                }
                let start = DispatchTime.now().uptimeNanoseconds
                let temporary = try MLModel.compileModel(at: URL(fileURLWithPath: source))
                let compileTime = seconds(start)
                let publishStart = DispatchTime.now().uptimeNanoseconds
                try FileManager.default.moveItem(at: temporary, to: target)
                return ["source": source, "compile_seconds": compileTime,
                        "cache_publish_seconds": seconds(publishStart)]
            }
            let config = MLModelConfiguration()
            config.computeUnits = .all
            let start = DispatchTime.now().uptimeNanoseconds
            let model = try MLModel(contentsOf: target, configuration: config)
            let loadTime = seconds(start)
            withExtendedLifetime(model) {}
            return ["compiled": destination, "load_seconds": loadTime]
        }
        rows.append(row)
    }
    let result: [String: Any] = ["mode": mode, "models": rows]
    let output = try JSONSerialization.data(withJSONObject: result, options: [.prettyPrinted, .sortedKeys])
    FileHandle.standardOutput.write(output)
}

do {
    try run()
} catch {
    fputs("\(error)\n", stderr)
    exit(1)
}
