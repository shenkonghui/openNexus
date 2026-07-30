// electron-builder afterSign 钩子：仅当提供完整 Apple 凭据（环境变量）时才对
// macOS 产物做公证；否则静默跳过，保证本地 / 无凭据 CI 仍能出未公证的 .dmg。
//
// 需要的环境变量：
//   APPLE_ID                     Apple 开发者账号邮箱
//   APPLE_APP_SPECIFIC_PASSWORD  该账号的 App 专用密码
//   APPLE_TEAM_ID                团队 ID
const path = require('path')

exports.default = async function notarizing(context) {
  const { electronPlatformName, appOutDir } = context
  if (electronPlatformName !== 'darwin') return

  const appleId = process.env.APPLE_ID
  const appleIdPassword = process.env.APPLE_APP_SPECIFIC_PASSWORD
  const teamId = process.env.APPLE_TEAM_ID
  if (!appleId || !appleIdPassword || !teamId) {
    console.log('[notarize] 未配置 APPLE_ID / APPLE_APP_SPECIFIC_PASSWORD / APPLE_TEAM_ID，跳过公证')
    return
  }

  // 延迟加载，避免未安装该可选依赖时在无需公证的场景报错
  const { notarize } = require('@electron/notarize')
  const appName = context.packager.appInfo.productFilename
  const appPath = path.join(appOutDir, `${appName}.app`)
  console.log(`[notarize] 正在公证 ${appName}.app ...`)
  await notarize({ appPath, appleId, appleIdPassword, teamId })
  console.log('[notarize] 公证完成')
}
