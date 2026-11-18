// chat.mjs <user> <password> — a person in a real browser: logs in through
// the gate, says something, then asks what they said first. The answer has
// to carry the first message back, which it can only do if the whole
// conversation went page → AG-UI → mind → LiteLLM → model.
//
// On the fake model the question's #first_message tag makes the answer
// exactly the first message; a real model reads the same question in words.
// Then a sum the model has a tool do: on the fake model #tool_sum makes it
// call everything's get-sum with 2 and 3, and the answer carries the tool's
// words back. Then the model thinks before it answers: the page shows its
// reasoning apart from the answer (#think_glm on the fake, in GLM's shape).
// Last, a sum in Gemini's shape (#think_gemini): reasoning, a tool call with
// thought signatures; the fake answers that the signatures came back with
// the call only if they did, and on the next run that both came back again
// from the page, which kept them as the reasoning's encrypted value. A real
// model is only asked to think and add; the signatures are the fake's check.
import { chromium } from 'playwright'

const [user, password] = process.argv.slice(2)
const url = process.env.LAB_URL
const first = process.env.E2E_FIRST ?? 'даром за амбаром'
const question = 'What was my very first message in this chat? Repeat it word for word. #first_message'
const sum = 'Use the get-sum tool to add 2 and 3, and tell me what it said. #tool_sum'
const think = 'Think it over first: is 1001 a prime number? #think_glm'
const signed = 'Think, then use the get-sum tool to add 2 and 3. #think_gemini'
const back = 'Did my signatures come back? #signatures_back'
const fake = (process.env.LAB_LLM_MODEL ?? 'fake') === 'fake'
const timeout = Number(process.env.E2E_TIMEOUT_MS ?? 60000)

const browser = await chromium.launch()
const page = await browser.newPage({ ignoreHTTPSErrors: true })
page.setDefaultTimeout(timeout)
let failed = false
const step = (label, value) => console.log(`  ${label.padEnd(36)} ${value}`)

// say writes text and waits for the run to end; the answer is what the
// assistant said since, which may be more than one message: a turn with a
// tool call is shown apart from the words that follow its result.
async function say(text) {
  const before = await page.locator('[data-role="assistant"]').count()
  await page.getByRole('textbox', { name: 'Message input' }).fill(text)
  await page.keyboard.press('Enter')
  await page.locator('[data-role="assistant"]').nth(before).waitFor()
  await page.getByRole('button', { name: 'Send message' }).waitFor()
  // The page types an answer out smoothly, behind the stream: the run is
  // over before the words are all on screen. Read once they stand still.
  let last = null
  for (;;) {
    const all = await page.locator('[data-role="assistant"]').allInnerTexts()
    const now = all.slice(before).join('\n').trim()
    if (now === last) return now
    last = now
    await page.waitForTimeout(500)
  }
}

// reasoned is how many reasoning blocks the page shows, and the words of the
// latest when opened.
async function reasoned() {
  const triggers = page.locator('[data-slot="reasoning-trigger"]')
  const n = await triggers.count()
  if (n === 0) return { n, text: '' }
  await triggers.nth(n - 1).click()
  const content = page.locator('[data-slot="reasoning-content"]').last()
  await content.waitFor()
  return { n, text: (await content.innerText()).trim() }
}

try {
  await page.goto(url + '/')
  step('no session: the login page', new URL(page.url()).pathname)
  await page.locator('#username-textfield').fill(user)
  await page.locator('#password-textfield').fill(password)
  await page.locator('#sign-in-button').click()
  await page.getByRole('textbox', { name: 'Message input' }).waitFor()
  step(`${user} logs in, the chat`, await page.title())

  const a1 = await say(first)
  step('an answer', JSON.stringify(a1.slice(0, 40)))
  const a2 = await say(question)
  step('the first message, asked back', JSON.stringify(a2.slice(0, 80)))
  if (!a2.toLowerCase().includes(first.toLowerCase())) {
    step('FAIL', `the answer does not carry ${JSON.stringify(first)}`)
    failed = true
  }
  const a3 = await say(sum)
  step('a sum, done by a tool', JSON.stringify(a3.slice(-80)))
  if (!a3.includes('5')) {
    step('FAIL', 'the answer does not carry the sum, 5')
    failed = true
  }

  const before = (await reasoned()).n
  const a4 = await say(think)
  const r = await reasoned()
  step('thinks, then answers', `${r.n - before} reasoning, ${JSON.stringify(r.text.slice(0, 40))}`)
  if (r.n <= before || !r.text) {
    step('FAIL', 'no reasoning shown before the answer')
    failed = true
  }
  if (fake && (!a4.includes('Thought it over.') || r.text.includes('Thought it over.'))) {
    step('FAIL', `reasoning and answer not apart: ${JSON.stringify(a4.slice(-80))}`)
    failed = true
  }

  const a5 = await say(signed)
  step('a sum, thought out, by a tool', JSON.stringify(a5.slice(-60)))
  const a6 = await say(back)
  step('the signatures, a run later', JSON.stringify(a6.slice(-60)))
  if (fake && !a5.includes('its signature came back')) {
    step('FAIL', 'the thought signatures did not go back with the tool call')
    failed = true
  }
  if (fake && !a6.includes('Both signatures came back')) {
    step('FAIL', 'the thought signatures did not come back from the page')
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
