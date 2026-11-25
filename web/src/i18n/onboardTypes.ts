// The shape of the words of the tour and the repository guide (see onboard/): the Chinese ones with their literals
// widened, so the English file must have exactly the same keys.

import type { onboardZhCN } from './onboard/zh-CN.ts'
import type { Widen } from './types.ts'

export type OnboardMessages = Widen<typeof onboardZhCN>
