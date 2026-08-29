import { reactive } from 'vue'
import { api } from '@/api/client'

export const auth = reactive({
  username: '',
  role: '',
  ready: false,

  init() {
    if (!api.isAuthed()) return
    api
      .me()
      .then((m) => {
        this.username = m.username
        this.role = m.role
      })
      .catch(() => api.logout())
      .finally(() => (this.ready = true))
  },

  async login(username: string, password: string) {
    const r = await api.login(username, password)
    this.username = r.username
    this.role = r.role
    this.ready = true
    return r
  },

  logout() {
    api.logout()
    this.username = ''
    this.role = ''
    location.href = '/login'
  },
})
