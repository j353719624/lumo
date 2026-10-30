<script setup lang="ts">
import { nextTick, ref } from 'vue'
import { locale, setLocale, t } from 'lumo-shared'
import { applyThemePref, type Theme } from './theme'

const theme = ref<Theme>((document.documentElement.dataset.theme as Theme) || 'dark')
function toggleTheme() {
  theme.value = theme.value === 'dark' ? 'light' : 'dark'
  applyThemePref(theme.value)
}

const account = ref('')
const password = ref('')
const showPwd = ref(false)
const state = ref<'idle' | 'loading' | 'done'>('idle')
const alertMsg = ref('')
const alertShow = ref(false)
const successShow = ref(false)
const twoFAStep = ref(false)
const twoFATicket = ref('')
const otpCode = ref('')
const otpInput = ref<HTMLInputElement | null>(null)
const langOpen = ref(false)

function pickLang(l: 'zh' | 'en') {
  setLocale(l)
  langOpen.value = false
}

function showError(msg: string) {
  alertMsg.value = msg
  alertShow.value = false
  void document.body.offsetWidth // 重新触发抖动动画
  alertShow.value = true
  successShow.value = false
}

function clearError() {
  alertShow.value = false
}

function backToPassword() {
  twoFAStep.value = false
  twoFATicket.value = ''
  otpCode.value = ''
  clearError()
  state.value = 'idle'
}

async function submit() {
  if (state.value !== 'idle') return

  const acc = account.value.trim()
  const pass = password.value

  if (!twoFAStep.value) {
    if (!acc) { showError(t('login.account') + '?'); return }
    if (!pass) { showError(t('login.password') + '?'); return }
  } else if (!/^\d{6}$/.test(otpCode.value.trim())) {
    showError(t('login.need2fa')); return
  }

  clearError()
  state.value = 'loading'

  try {
    if (!twoFAStep.value) {
      const res = await fetch('api/login', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ account: acc, password: pass }),
      })
      const d = await res.json()
      if (res.ok && d.need2fa) {
        twoFAStep.value = true
        twoFATicket.value = d.ticket || ''
        state.value = 'idle'
        clearError()
        await nextTick()
        otpInput.value?.focus()
        return
      }
      if (res.ok) {
        finish(acc)
        return
      }
      state.value = 'idle'
      showError(d.error || '×')
    } else {
      const res = await fetch('api/login/2fa', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ ticket: twoFATicket.value, code: otpCode.value.trim() }),
      })
      const d = await res.json()
      if (res.ok) {
        finish(acc)
        return
      }
      state.value = 'idle'
      otpCode.value = ''
      showError(d.error || '×')
    }
  } catch {
    state.value = 'idle'
    showError('Network error')
  }
}

function finish(acc: string) {
  state.value = 'done'
  successShow.value = true
  setTimeout(() => { location.href = './' }, 350)
}

</script>

<template>
  <div class="app">
    <header class="loginbar">
      <div class="brandmark">
        <svg width="32" height="32" viewBox="0 0 72 72" fill="none" aria-hidden="true">
          <path d="M22 57V38C22 26.9 28.3 18.4 36 18.4C43.7 18.4 50 26.9 50 38V57" stroke="#5A7BFF" stroke-width="6.6" stroke-linecap="round"/>
          <path d="M10 42H62" stroke="#35E0C8" stroke-width="4.4" stroke-linecap="round"/>
          <circle cx="10" cy="42" r="3.9" fill="#35E0C8"/><circle cx="62" cy="42" r="3.9" fill="#35E0C8"/>
        </svg>
        <span class="wm">LUMO</span>
        <span class="divider"></span>
        <span class="cn">隧光</span>
      </div>
      <div class="bartools">
  <button class="themefab" type="button" :title="theme === 'dark' ? '日间模式' : '黑夜模式'" @click="toggleTheme">
    <svg v-if="theme === 'dark'" width="16" height="16" viewBox="0 0 16 16" fill="none"><circle cx="8" cy="8" r="3.2" stroke="currentColor" stroke-width="1.4"/><path d="M8 1.2V3M8 13V14.8M1.2 8H3M13 8H14.8M3.2 3.2L4.5 4.5M11.5 11.5L12.8 12.8M12.8 3.2L11.5 4.5M4.5 11.5L3.2 12.8" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/></svg>
    <svg v-else width="16" height="16" viewBox="0 0 16 16" fill="none"><path d="M13.4 9.6A5.8 5.8 0 0 1 6.4 2.6A5.8 5.8 0 1 0 13.4 9.6Z" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/></svg>
  </button>
      <div class="langsel" :class="{ open: langOpen }">
        <button class="langbtn" type="button" @click.stop="langOpen = !langOpen">
          <span class="flag">{{ locale === 'zh' ? '🇨🇳' : '🇺🇸' }}</span>
          <span>{{ locale === 'zh' ? '简体中文' : 'English' }}</span>
          <svg class="caret" width="9" height="9" viewBox="0 0 10 10" fill="none">
            <path d="M2 3.8L5 6.8L8 3.8" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/>
          </svg>
        </button>
        <div class="langmenu" v-if="langOpen">
          <button type="button" :class="{ cur: locale === 'zh' }" @click="pickLang('zh')"><span>🇨🇳</span> 简体中文</button>
          <button type="button" :class="{ cur: locale === 'en' }" @click="pickLang('en')"><span>🇺🇸</span> English</button>
        </div>
      </div>
      </div>
    </header>

    <main class="stage">
    <!-- 左：品牌 -->
    <section class="brandside">
      <svg class="deco" viewBox="0 0 720 720" fill="none" aria-hidden="true">
        <path d="M132 706V382C132 202 236 100 360 100C484 100 588 202 588 382V706" stroke="url(#dg)" stroke-width="1.6"/>
        <path d="M148 634V390C148 226 244 128 360 128C476 128 572 226 572 390V634" stroke="url(#dg2)" stroke-width="1.2"/>
        <path d="M0 438H720" stroke="#35E0C8" stroke-opacity=".13" stroke-width="1.1"/>
        <circle cx="0" cy="438" r="4" fill="#35E0C8" fill-opacity=".28"/>
        <circle cx="720" cy="438" r="4" fill="#35E0C8" fill-opacity=".28"/>
        <circle cx="360" cy="438" r="2" fill="#35E0C8" fill-opacity=".2"/>
        <defs>
          <linearGradient id="dg" x1="132" y1="100" x2="588" y2="706" gradientUnits="userSpaceOnUse">
            <stop stop-color="#5A7BFF" stop-opacity=".26"/>
            <stop offset="1" stop-color="#35E0C8" stop-opacity=".05"/>
          </linearGradient>
          <linearGradient id="dg2" x1="148" y1="128" x2="572" y2="634" gradientUnits="userSpaceOnUse">
            <stop stop-color="#5A7BFF" stop-opacity=".1"/>
            <stop offset="1" stop-color="#35E0C8" stop-opacity=".03"/>
          </linearGradient>
        </defs>
      </svg>

      <div class="pitch">
        <h1>{{ t('pitch.line1') }}<br>{{ t('pitch.line2pre') }}<em>{{ t('pitch.line2em') }}</em>{{ t('pitch.line2post') }}</h1>
        <p>{{ t('pitch.sub') }}</p>

        <div class="feats">
          <div class="feat">
            <span class="ic">
              <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                <path d="M8 1.8V7.2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                <path d="M4.6 4.2A5.2 5.2 0 1 0 11.4 4.2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
              </svg>
            </span>
            <div><b>{{ t('feat.1t') }}</b><span>{{ t('feat.1d') }}</span></div>
          </div>
          <div class="feat">
            <span class="ic">
              <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                <path d="M2.2 5.4H13.8M2.2 10.6H13.8" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>
                <circle cx="6" cy="5.4" r="1.7" fill="#151B26" stroke="currentColor" stroke-width="1.4"/>
                <circle cx="10.2" cy="10.6" r="1.7" fill="#151B26" stroke="currentColor" stroke-width="1.4"/>
              </svg>
            </span>
            <div><b>{{ t('feat.2t') }}</b><span>{{ t('feat.2d') }}</span></div>
          </div>
          <div class="feat">
            <span class="ic">
              <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                <path d="M8 2.2L13 4.1V8.2C13 11.1 10.9 13.1 8 14.1C5.1 13.1 3 11.1 3 8.2V4.1L8 2.2Z" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/>
                <path d="M6.1 8L7.5 9.4L10 6.7" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/>
              </svg>
            </span>
            <div><b>{{ t('feat.3t') }}</b><span>{{ t('feat.3d') }}</span></div>
          </div>
        </div>
      </div>

      <p class="pitch-mobile">{{ t('pitch.short') }}</p>
    </section>

    <!-- 右：登录表单 -->
    <section class="formside">
      <div class="panel">
        <h2>{{ twoFAStep ? t('login.2fa') : t('login.title') }}</h2>
        <p class="sub">{{ twoFAStep ? t('login.need2fa') : t('login.sub') }}</p>

        <form @submit.prevent="submit" novalidate>
          <template v-if="!twoFAStep">
          <div class="field">
            <label for="acc">{{ t('login.account') }}</label>
            <div class="ctrl">
              <span class="lead">
                <svg width="15" height="15" viewBox="0 0 16 16" fill="none">
                  <circle cx="8" cy="5.4" r="2.7" stroke="currentColor" stroke-width="1.4"/>
                  <path d="M3 13.4C3.5 11.2 5.5 9.8 8 9.8C10.5 9.8 12.5 11.2 13 13.4" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>
                </svg>
              </span>
              <input id="acc" v-model="account" type="text" inputmode="email"
                     placeholder="邮箱或用户名" autocomplete="username" spellcheck="false" autofocus>
            </div>
          </div>

          <div class="field">
            <label for="pwd">{{ t('login.password') }}</label>
            <div class="ctrl has-eye">
              <span class="lead">
                <svg width="15" height="15" viewBox="0 0 16 16" fill="none">
                  <rect x="3" y="7.2" width="10" height="6.6" rx="1.8" stroke="currentColor" stroke-width="1.4"/>
                  <path d="M5.7 7.2V5.4C5.7 4.1 6.7 3.1 8 3.1C9.3 3.1 10.3 4.1 10.3 5.4V7.2" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>
                  <circle cx="8" cy="10.5" r="1" fill="currentColor"/>
                </svg>
              </span>
              <input id="pwd" v-model="password" :type="showPwd ? 'text' : 'password'"
                     placeholder="请输入密码" autocomplete="current-password">
              <button class="eye" type="button" :aria-pressed="showPwd ? 'true' : 'false'" aria-label="显示密码" @click="showPwd = !showPwd">
                <svg class="on" width="16" height="16" viewBox="0 0 16 16" fill="none">
                  <path d="M1.6 8C3.4 4.9 5.6 3.4 8 3.4C10.4 3.4 12.6 4.9 14.4 8C12.6 11.1 10.4 12.6 8 12.6C5.6 12.6 3.4 11.1 1.6 8Z" stroke="currentColor" stroke-width="1.4" stroke-linejoin="round"/>
                  <circle cx="8" cy="8" r="2.1" fill="currentColor"/>
                </svg>
                <svg class="off" width="16" height="16" viewBox="0 0 16 16" fill="none">
                  <path d="M2.6 5.5C2 6.2 1.7 7 1.6 8C3.4 11.1 5.6 12.6 8 12.6C9 12.6 10 12.3 10.9 11.7M6.2 3.7C6.8 3.5 7.4 3.4 8 3.4C10.4 3.4 12.6 4.9 14.4 8C14 8.7 13.5 9.3 13 9.8" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round"/>
                  <path d="M2.6 2.6L13.4 13.4" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>
                </svg>
              </button>
            </div>
          </div>

          </template>

          <div class="field" v-if="twoFAStep">
            <label for="otp">{{ t('login.2fa') }}</label>
            <div class="ctrl">
              <span class="lead">
                <svg width="15" height="15" viewBox="0 0 16 16" fill="none">
                  <rect x="3" y="2.5" width="10" height="11" rx="2" stroke="currentColor" stroke-width="1.4"/>
                  <path d="M6 12.8H10" stroke="currentColor" stroke-width="1.4" stroke-linecap="round"/>
                </svg>
              </span>
              <input id="otp" ref="otpInput" v-model="otpCode" type="text" inputmode="numeric"
                     maxlength="6" :placeholder="t('login.2faPlaceholder')" autocomplete="one-time-code">
            </div>
          </div>

          <div class="alert" :class="{ show: alertShow }" role="alert" aria-live="polite">
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
              <circle cx="8" cy="8" r="6.4" stroke="#FF5F73" stroke-width="1.4"/>
              <path d="M8 4.9V8.7" stroke="#FF5F73" stroke-width="1.6" stroke-linecap="round"/>
              <circle cx="8" cy="11.1" r=".95" fill="#FF5F73"/>
            </svg>
            <span>{{ alertMsg || t('login.need2fa') }}</span>
          </div>

          <div class="success" :class="{ show: successShow }">
            <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
              <circle cx="8" cy="8" r="6.4" stroke="#3DDC97" stroke-width="1.4"/>
              <path d="M5.2 8.2L7.1 10.1L10.8 6.2" stroke="#3DDC97" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/>
            </svg>
            <span>{{ t('login.success') }}</span>
            <a href="/">{{ t('login.enter') }}</a>
          </div>

          <button class="submit" type="submit" :data-state="state" :disabled="state !== 'idle'">
            <span class="spinner" aria-hidden="true"></span>
            <span class="label">{{ state === 'loading' ? t('login.loading') : state === 'done' ? t('login.done') : twoFAStep ? t('login.verify') : t('login.submit') }}</span>
          </button>
          <button v-if="twoFAStep" class="linkback" type="button" @click="backToPassword">{{ t('login.back') }}</button>
        </form>
      </div>
    </section>
    </main>

  </div>
</template>

<style src="./login.css"></style>
