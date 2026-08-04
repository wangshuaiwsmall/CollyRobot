export type LogLevel = 'DEBUG' | 'INFO' | 'WARN' | 'ERROR'
export type LogContext = Record<string, unknown>

interface LogEntry {
  level: LogLevel
  message: string
  timestamp: string
  context: LogContext
}

/**
 * FrontendLogger 将浏览器事件上报给后端，由后端写入独立的 frontend-日期.log。
 * 浏览器没有直接写服务器文件的权限，因此日志文件轮转统一在服务端完成。
 */
class FrontendLogger {
  private readonly endpoint = '/api/logs/frontend'

  debug(message: string, context: LogContext = {}) {
    this.write('DEBUG', message, context)
  }

  info(message: string, context: LogContext = {}) {
    this.write('INFO', message, context)
  }

  warn(message: string, context: LogContext = {}) {
    this.write('WARN', message, context)
  }

  error(message: string, context: LogContext = {}) {
    this.write('ERROR', message, context)
  }

  private write(level: LogLevel, message: string, context: LogContext) {
    const entry: LogEntry = {
      level,
      message,
      timestamp: new Date().toISOString(),
      context: this.toSerializable(context),
    }

    // 开发者工具保留一份输出，便于前端开发时即时定位问题。
    const consoleMethod = level === 'ERROR' ? console.error : level === 'WARN' ? console.warn : console.log
    consoleMethod(`[${level}] ${message}`, entry.context)

    // keepalive 允许页面关闭过程中尽量完成短日志请求；上报失败不能影响正常业务流程。
    void fetch(this.endpoint, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(entry),
      keepalive: true,
    }).catch((reason: unknown) => {
      console.warn('[LOGGER] 前端日志上报失败', reason)
    })
  }

  /** 将 Error 和不可序列化值转换为适合 JSON 上报的普通数据。 */
  private toSerializable(context: LogContext): LogContext {
    try {
      return JSON.parse(
        JSON.stringify(context, (_key, value: unknown) => {
          if (value instanceof Error) {
            return { name: value.name, message: value.message, stack: value.stack }
          }
          if (typeof value === 'bigint') return value.toString()
          return value
        }),
      ) as LogContext
    } catch {
      return { serialization_error: '日志上下文无法序列化' }
    }
  }
}

export const logger = new FrontendLogger()

