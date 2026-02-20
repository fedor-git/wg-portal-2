<template>
  <Teleport to="#modals">
    <div v-show="visible" class="modal-backdrop fade show" @click="closeBackdrop">
      <div class="modal fade show" tabindex="-1">
        <div class="modal-dialog modal-lg modal-dialog-centered" ref="modalDialog" @click.stop="">
          <!-- <div class="modal-dialog modal-lg modal-dialog-centered modal-dialog-scrollable" @click.stop=""> -->
          <div class="modal-content" ref="modalContent">
            <div class="modal-header">
              <h5 class="modal-title">{{ title }}</h5>
              <button @click="closeModal" class="btn-close" aria-label="Close"></button>
            </div>
            <div class="modal-body col-md-12">
              <slot></slot>
            </div>
            <div class="modal-footer">
              <slot name="footer"></slot>
            </div>
          </div>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style>
.modal.show {
  display:block;
}
.modal.show {
  opacity: 1;
}
.modal-backdrop {
  position: fixed;
  top: 0;
  left: 0;
  width: 100%;
  height: 100%;
  background-color: rgba(0,0,0,0.3) !important;
  z-index: 1040;
}
.modal-backdrop.show {
  opacity: 1 !important;
}
.modal {
  z-index: 1050;
}
.modal-dialog {
  height: auto !important;
  max-height: none !important;
}
.modal-body {
  max-height: 80vh;
  overflow-y: auto;
}
.modal-content {
  box-shadow: 0 0 30px rgba(0, 0, 0, 0.3);
}
</style>

<script setup>
import { ref, watch, nextTick } from 'vue'

const props = defineProps({
  title: String,
  visible: Boolean,
  closeOnBackdrop: Boolean,
  minHeight: {
    type: Number,
    default: 300
  }
})

const emit = defineEmits(['close'])
const modalDialog = ref(null)
const modalContent = ref(null)

// Встановлює висоту модального вікна на основі контенту
const setModalHeight = async () => {
  // Використовуємо requestAnimationFrame для синхронізації з браузером
  return new Promise((resolve) => {
    requestAnimationFrame(() => {
      if (!modalContent.value || !modalDialog.value) {
        resolve()
        return
      }
      
      const contentHeight = modalContent.value.scrollHeight
      const finalHeight = Math.max(contentHeight, props.minHeight)
      
      modalDialog.value.style.minHeight = `${finalHeight}px`
      resolve()
    })
  })
}

// При відкритті модалі встановлює висоту
watch(() => props.visible, async (newVal) => {
  if (newVal) {
    await setModalHeight()
    
    // ResizeObserver для спостереження за розміром
    const resizeObserver = new ResizeObserver(() => {
      setModalHeight()
    })
    
    // MutationObserver для спостереження за змінами DOM (вкладки)
    const mutationObserver = new MutationObserver(() => {
      // Чекаємо 400ms щоб дозволити анімації Bootstrap вкладок закінчитися
      // і браузеру завершити layout recalculation
      setTimeout(async () => {
        await setModalHeight()
      }, 400)
    })
    
    if (modalContent.value) {
      resizeObserver.observe(modalContent.value)
      // Спостерігаємо за змінами класів (розкриття/закриття вкладок)
      mutationObserver.observe(modalContent.value, {
        attributes: true,
        subtree: true,
        attributeFilter: ['class']
      })
      
      // Зберігаємо observers щоб їх потім очистити
      modalContent.value.__resizeObserver = resizeObserver
      modalContent.value.__mutationObserver = mutationObserver
    }
  } else {
    // Очищуємо observers при закритті модалі
    if (modalContent.value?.__resizeObserver) {
      modalContent.value.__resizeObserver.disconnect()
    }
    if (modalContent.value?.__mutationObserver) {
      modalContent.value.__mutationObserver.disconnect()
    }
  }
})

function closeBackdrop() {
  if(props.closeOnBackdrop) {
    console.log("CLOSING BD")
    emit('close')
  }
}

function closeModal() {
  console.log("CLOSING")
  emit('close')
}
</script>