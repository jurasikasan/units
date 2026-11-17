// chat.mjs <user> <password> — a person in a real browser: logs in through
// the gate, says something, then asks what they said first. The answer has
// to carry the first message back, which it can only do if the whole
// conversation went page → AG-UI → mind → LiteLLM → model.
//
// On the fake model the question's #first_message tag makes the answer
// exactly the first message; a real model reads the same question in words.
import { chromium } from 'playwright'

const [user, password] = process.argv.slice(2)
const url = process.env.LAB_URL
const first = process.env.E2E_FIRST ?? 'даром за амбаром'
const question = 'What was my very first message in this chat? Repeat it word for word. #first_message'
const timeout = Number(process.env.E2E_TIMEOUT_MS ?? 60000)

const browser = await chromium.launch()
const page = await browser.newPage({ ignoreHTTPSErrors: true })
page.setDefaultTimeout(timeout)
let failed = false
const step = (label, value) => console.log(`  ${label.padEnd(36)} ${value}`)

async function say(text, n) {
  await page.getByRole('textbox', { name: 'Message input' }).fill(text)
  await page.keyboard.press('Enter')
  const answer = page.locator('[data-role="assistant"]').nth(n - 1)
  await answer.waitFor()
  await page.getByRole('button', { name: 'Send message' }).waitFor()
  return (await answer.innerText()).trim()
}

try {
  await page.goto(url + '/')
  step('no session: the login page', new URL(page.url()).pathname)
  await page.locator('#username-textfield').fill(user)
  await page.locator('#password-textfield').fill(password)
  await page.locator('#sign-in-button').click()
  await page.getByRole('textbox', { name: 'Message input' }).waitFor()
  step(`${user} logs in, the chat`, await page.title())

  const a1 = await say(first, 1)
  step('an answer', JSON.stringify(a1.slice(0, 40)))
  const a2 = await say(question, 2)
  step('the first message, asked back', JSON.stringify(a2.slice(0, 80)))
  if (!a2.toLowerCase().includes(first.toLowerCase())) {
    step('FAIL', `the answer does not carry ${JSON.stringify(first)}`)
    failed = true
  }
} catch (e) {
  console.log(`  FAIL ${e.message.split('\n')[0]}`)
  await page.screenshot({ path: '/tmp/e2e-fail.png' }).catch(() => {})
  failed = true
} finally {
  await browser.close()
}
process.exit(failed ? 1 : 0)
