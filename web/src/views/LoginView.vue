<script setup lang="ts">
import { ref } from 'vue'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { auth } from '@/stores/auth'

const router = useRouter()
const username = ref('')
const password = ref('')
const loading = ref(false)
const error = ref('')

async function onSubmit() {
  error.value = ''
  if (!username.value || !password.value) {
    error.value = '请输入用户名和密码'
    return
  }
  loading.value = true
  try {
    await auth.login(username.value, password.value)
    ElMessage.success('登录成功')
    router.replace('/nodes')
  } catch (e) {
    error.value = (e as Error).message || '登录失败'
  } finally {
    loading.value = false
  }
}
</script>

<template>
  <div class="login-wrap">
    <div class="login-card">
      <div class="login-brand">
        <span class="brand-dot" />
        边缘节点控制台
      </div>
      <p class="login-sub">控制面按需上线 · 节点常在线自治</p>

      <el-form @submit.prevent="onSubmit">
        <el-form-item>
          <el-input v-model="username" placeholder="用户名" size="large" />
        </el-form-item>
        <el-form-item>
          <el-input
            v-model="password"
            type="password"
            placeholder="密码"
            size="large"
            show-password
            @keyup.enter="onSubmit"
          />
        </el-form-item>
        <div v-if="error" class="login-error">{{ error }}</div>
        <el-button type="primary" size="large" style="width: 100%" :loading="loading" @click="onSubmit">
          登录
        </el-button>
      </el-form>
      <div class="login-foot">默认超管 admin · 首次启动已生成随机口令</div>
    </div>
  </div>
</template>

<style scoped>
.login-wrap {
  height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  background: linear-gradient(135deg, #f3eefe 0%, #fdf2f7 100%);
}
.login-card {
  width: 360px;
  padding: 36px 32px;
  background: #fff;
  border-radius: 16px;
  box-shadow: 0 12px 40px rgba(107, 55, 201, 0.12);
}
.login-brand {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 20px;
  font-weight: 600;
  color: #4a2b8c;
}
.login-sub {
  color: #8a8aa0;
  font-size: 13px;
  margin: 6px 0 24px;
}
.login-error {
  color: #d93a3a;
  font-size: 13px;
  margin-bottom: 12px;
}
.login-foot {
  margin-top: 16px;
  font-size: 12px;
  color: #aaa;
  text-align: center;
}
.brand-dot {
  width: 12px;
  height: 12px;
  border-radius: 4px;
  background: #6b37c9;
  display: inline-block;
}
</style>
