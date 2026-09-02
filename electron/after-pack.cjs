// 构建后钩子：移除 ElectronAsarIntegrity 键
// electron-builder 25.x + Electron 33 在未签名构建时 ASAR 完整性 hash 不匹配，
// 会导致 macOS 静默退出。未签名应用无需 ASAR 完整性校验。
const { execSync } = require('child_process')

exports.default = async function (context) {
  const plistPath = context.appOutContents + '/Info.plist'
  try {
    execSync(`/usr/libexec/PlistBuddy -c 'Delete ElectronAsarIntegrity' '${plistPath}'`, { stdio: 'ignore' })
    console.log('[after-pack] 已移除 ElectronAsarIntegrity（未签名构建无需校验）')
  } catch {
    // 键不存在则跳过
  }
}
