import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { CaseStatus, IncidentCaseDetail } from '../lib/bridge/bugWorkflow'
import BugCaseLifecycle, { primaryActionFor } from './BugCaseLifecycle.vue'
const pickEvidence = vi.hoisted(() => vi.fn())
vi.mock('../lib/bridge/bugWorkflow', async original => ({ ...(await original<object>()), selectIncidentEvidence: pickEvidence }))
afterEach(() => { delete (window as any).go; pickEvidence.mockReset() })
function detail(status: CaseStatus = 'waiting_fix_approval'): IncidentCaseDetail {
 return { case:{id:'case-1',bug_id:'bug-1',source:'zentao',system_id:'base',environment:'test',status,cycle_number:1,current_attempt_id:'root-1',selected_bot_key:'base|codex',version:4,created_at:'',updated_at:''},
 attempts:[{id:'root-1',case_id:'case-1',cycle_number:1,phase:'investigation',mode:'',status:'succeeded',agent_target:'codex',bot_key:'base|codex',input_json:{},output_json:{investigation_status:'root_cause_ready',remediation:{repositories:['api'],mode:'code_change'}},parent_attempt_id:'',started_at:'',error_code:'',error_message:'',usage:{}}],artifacts:[],approvals:[],code_changes:[],deployment_observations:[],events:[] }
}
const mountCase=(snapshot:IncidentCaseDetail,extra={})=>mount(BugCaseLifecycle,{props:{detail:snapshot,...extra},global:{stubs:{BugCaseArtifacts:true,BugAgentProgress:true}}})
describe('investigation, fix and submission lifecycle',()=>{
 it.each([['pending_investigation','start_investigation'],['investigating','cancel_attempt'],['waiting_evidence','supply_evidence'],['waiting_fix_approval','approve_fix'],['waiting_remediation','complete_remediation'],['fix_failed','continue_fix'],['waiting_merge_approval','approve_merge'],['merge_conflict','supply_merge_decision']] as const)('maps %s to %s',(status,kind)=>expect(primaryActionFor(detail(status))?.kind).toBe(kind))
 it.each(['submitted','remediation_recorded','fixed_verified','validating','regression_validating'] as CaseStatus[])('does not execute terminal or retired phase %s',status=>expect(primaryActionFor(detail(status))).toBeUndefined())
 it('shows submission awaiting human verification',()=>{const w=mountCase(detail('submitted'));expect(w.text()).toContain('已提交，待人工验证');expect(w.text()).toContain('请由人工验收并更新工单状态');expect(w.find('[data-primary-action]').exists()).toBe(false)})
 it('binds fix approval to the visible root cause, version and baseline',async()=>{
 const load=vi.fn().mockResolvedValue({api:['feature/work']});const w=mountCase(detail(),{loadFixBranches:load});await w.get('[data-primary-action]').trigger('click');await flushPromises();expect(load).toHaveBeenCalledWith('case-1','root-1');expect(w.emitted('primary')).toBeUndefined();await w.get('.source-baseline-row input').setValue('feature/work');await w.get('[data-dialog-confirm]').trigger('click');expect(w.emitted('primary')?.[0][0]).toMatchObject({kind:'approve_fix',rootCauseAttemptID:'root-1',caseVersion:4,sourceBaselines:{api:'feature/work'}})
 })
 it('rejects fix approval without repository scope',async()=>{const d=detail();d.attempts[0].output_json={};const w=mountCase(d);await w.get('[data-primary-action]').trigger('click');expect(w.get('[data-dialog-confirm]').attributes('disabled')).toBeDefined()})
 it('closes stale approval when version changes',async()=>{const d=detail();const w=mountCase(d);await w.get('[data-primary-action]').trigger('click');await w.setProps({detail:{...d,case:{...d.case,version:5}}});expect(w.find('[role="dialog"]').exists()).toBe(false);expect(w.emitted('primary')).toBeUndefined()})
 it('ignores a branch response after switching case',async()=>{let resolve!:(v:Record<string,string[]>)=>void;const w=mountCase(detail(),{loadFixBranches:()=>new Promise<Record<string,string[]>>(r=>resolve=r)});await w.get('[data-primary-action]').trigger('click');const d=detail();d.case.id='case-2';await w.setProps({detail:d});resolve({api:['stale']});await flushPromises();expect(w.find('[role="dialog"]').exists()).toBe(false);expect(w.emitted('primary')).toBeUndefined()})
 it('shows exact commit and target before separate merge approval',async()=>{const d=detail('waiting_merge_approval');d.case.current_attempt_id='fix-1';d.code_changes=[{id:'change',case_id:'case-1',attempt_id:'fix-1',repo:'api',base_branch:'main',fix_branch:'fix/bug',fix_commit:'fix-sha',test_evidence:[],target_environment_branch:'test',merge_base_head:'target-sha',merge_commit:'',push_remote:'origin',push_status:'pushed'}];const w=mountCase(d);await w.get('[data-primary-action]').trigger('click');expect(w.get('[role="dialog"]').text()).toContain('fix-sha');expect(w.get('[role="dialog"]').text()).toContain('target-sha');expect(w.emitted('primary')).toBeUndefined();await w.get('[data-dialog-confirm]').trigger('click');expect(w.emitted('primary')?.[0][0]).toMatchObject({kind:'approve_merge',caseVersion:4})})
 it('requires evidence before resuming',async()=>{const w=mountCase(detail('waiting_evidence'));await w.get('[data-primary-action]').trigger('click');expect(w.get('[data-dialog-confirm]').attributes('disabled')).toBeDefined();await w.get('textarea').setValue('request id req-42');await w.get('[data-dialog-confirm]').trigger('click');expect(w.emitted('primary')?.[0][0]).toMatchObject({kind:'supply_evidence',input:'request id req-42'})})
 it('records human remediation with supporting evidence',async()=>{const w=mountCase(detail('waiting_remediation'));await w.get('[data-primary-action]').trigger('click');await w.findAll('textarea')[0].setValue('恢复配置');expect(w.get('[data-dialog-confirm]').attributes('disabled')).toBeDefined();await w.findAll('textarea')[1].setValue('OPS-1');await w.get('[data-dialog-confirm]').trigger('click');expect(w.emitted('primary')?.[0][0]).toMatchObject({kind:'complete_remediation',input:'恢复配置',evidence:'OPS-1'})})
 it('blocks duplicate pending commands',async()=>{const w=mountCase(detail(),{pending:true});expect(w.get('[data-primary-action]').attributes('disabled')).toBeDefined();await w.get('[data-primary-action]').trigger('click');expect(w.find('[role="dialog"]').exists()).toBe(false)})
})

describe('task-focused layout', () => {
 it.each([['investigating', '排障'], ['waiting_fix_approval', '修复'], ['waiting_merge_approval', '提交']] as const)('highlights the current stage for %s', (status, label) => {
   const wrapper = mountCase(detail(status))
   expect(wrapper.get('.stages [aria-current="step"]').text()).toContain(label)
   expect(wrapper.findAll('.stages li')).toHaveLength(3)
 })
 it('does not mark archived workflows as active investigation', () => {
   const wrapper = mountCase(detail('legacy_archived'))
   expect(wrapper.find('.stages [aria-current]').exists()).toBe(false)
 })
 it('removes empty operation history and completes the submission stepper', () => {
   const wrapper = mountCase(detail('submitted'))
   expect(wrapper.find('.timeline').exists()).toBe(false)
   expect(wrapper.findAll('.stages .done')).toHaveLength(3)
   expect(wrapper.find('.stages [aria-current]').exists()).toBe(false)
 })
})

describe('native evidence picker', () => {
 const image = { name: '截图.png', mime_type: 'image/png', base64_data: 'cG5n' }
 async function openPicker() {
   ;(window as any).go = {}
   const wrapper = mountCase(detail('waiting_evidence'))
   await wrapper.get('[data-primary-action]').trigger('click')
   return wrapper
 }
 it('uses the desktop picker and enables attachment-only continuation', async () => {
   pickEvidence.mockResolvedValue({ images: [image], files: [] })
   const wrapper = await openPicker()
   expect(wrapper.get('input[type=file]').attributes('hidden')).toBeDefined()
   await wrapper.get('[data-select-evidence]').trigger('click'); await flushPromises()
   expect(pickEvidence).toHaveBeenCalledOnce()
   expect(wrapper.get('.attachment-count').text()).toContain('1 张截图')
   expect(wrapper.get('.attachment-list').text()).toContain('截图.png')
   expect(wrapper.get('[data-dialog-confirm]').attributes('disabled')).toBeUndefined()
   await wrapper.get('[data-dialog-confirm]').trigger('click')
   expect(wrapper.emitted('primary')?.[0][0]).toMatchObject({ kind: 'supply_evidence', images: [image] })
 })
 it('keeps earlier attachments when the next picker is cancelled', async () => {
   pickEvidence.mockResolvedValueOnce({ images: [image], files: [] }).mockResolvedValueOnce({ images: [], files: [] })
   const wrapper = await openPicker()
   await wrapper.get('[data-select-evidence]').trigger('click'); await flushPromises()
   await wrapper.get('[data-select-evidence]').trigger('click'); await flushPromises()
   expect(wrapper.findAll('.attachment-list li')).toHaveLength(1)
   await wrapper.get('.attachment-list button').trigger('click')
   expect(wrapper.get('[data-dialog-confirm]').attributes('disabled')).toBeDefined()
 })
 it('blocks concurrent selection and ignores results after switching case', async () => {
   let resolve!: (value: unknown) => void
   pickEvidence.mockImplementation(() => new Promise(r => { resolve = r }))
   const wrapper = await openPicker()
   await wrapper.get('[data-select-evidence]').trigger('click')
   expect(wrapper.get('[data-select-evidence]').attributes('disabled')).toBeDefined()
   expect(wrapper.get('[data-dialog-confirm]').attributes('disabled')).toBeDefined()
   const next = detail('waiting_evidence'); next.case.id = 'case-2'
   await wrapper.setProps({ detail: next })
   resolve({ images: [image], files: [] }); await flushPromises()
   await wrapper.get('[data-primary-action]').trigger('click')
   expect(wrapper.find('.attachment-list').exists()).toBe(false)
 })
 it('reports picker failure in the dialog and permits retry', async () => {
   pickEvidence.mockRejectedValueOnce(new Error('无法打开文件选择窗口')).mockResolvedValueOnce({ images: [image], files: [] })
   const wrapper = await openPicker()
   await wrapper.get('[data-select-evidence]').trigger('click'); await flushPromises()
   expect(wrapper.get('[role=alert]').text()).toContain('无法打开文件选择窗口')
   await wrapper.get('[data-select-evidence]').trigger('click'); await flushPromises()
   expect(wrapper.find('[role=alert]').exists()).toBe(false)
   expect(wrapper.findAll('.attachment-list li')).toHaveLength(1)
 })
 it('enforces per-kind limits across selections without adding a partial batch', async () => {
   pickEvidence.mockResolvedValueOnce({ images: Array(4).fill(image), files: [] }).mockResolvedValueOnce({ images: [image], files: [{ name:'log.txt', mime_type:'text/plain', base64_data:'bG9n' }] })
   const wrapper = await openPicker()
   await wrapper.get('[data-select-evidence]').trigger('click'); await flushPromises()
   await wrapper.get('[data-select-evidence]').trigger('click'); await flushPromises()
   expect(wrapper.get('[role=alert]').text()).toContain('各最多添加 4 个')
   expect(wrapper.findAll('.attachment-list li')).toHaveLength(4)
 })
})
