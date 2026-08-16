package com.callscreen.feature.business

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material.icons.Icons
import androidx.compose.material.icons.filled.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

import androidx.compose.material.icons.automirrored.filled.ArrowBack

@OptIn(ExperimentalMaterial3Api::class)
@Composable
public fun AiOpsStudioScreen(
    onBackClick: () -> Unit = {},
    modifier: Modifier = Modifier
) {
    val darkBg = Color(0xFF0F172A)
    val cardBg = Color(0xFF1E293B)
    val textLight = Color(0xFFF8FAFC)

    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text("Aegis Ops Studio", style = MaterialTheme.typography.titleMedium, fontWeight = FontWeight.Bold, color = textLight) },
                navigationIcon = {
                    IconButton(onClick = onBackClick) {
                        Icon(Icons.AutoMirrored.Filled.ArrowBack, contentDescription = "Back", tint = textLight)
                    }
                },
                colors = TopAppBarDefaults.topAppBarColors(containerColor = darkBg)
            )
        }
    ) { padding ->
        LazyColumn(
            modifier = modifier
                .fillMaxSize()
                .padding(padding)
                .background(darkBg)
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(16.dp)
        ) {
            // Environment Selector Tabs
            item {
                Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    FilterChip(selected = true, onClick = {}, label = { Text("Prod", fontSize = 12.sp) })
                    FilterChip(selected = false, onClick = {}, label = { Text("Staging", fontSize = 12.sp) })
                    FilterChip(selected = false, onClick = {}, label = { Text("Dev", fontSize = 12.sp) })
                }
            }

            // Active Prompts Section
            item {
                Card(
                    shape = RoundedCornerShape(16.dp),
                    colors = CardDefaults.cardColors(containerColor = cardBg),
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                        Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween, verticalAlignment = Alignment.CenterVertically) {
                            Text("Active Prompts", fontWeight = FontWeight.Bold, fontSize = 15.sp, color = textLight)
                            Text("View All", fontSize = 12.sp, color = Color(0xFF818CF8))
                        }

                        PromptCardItem("v2.4", "Receptionist_Default", "System prompt for external routing", "PUBLISHED", Color(0xFF0284C7))
                        PromptCardItem("v1.8", "Data_Extraction_Agent", "JSON structifier for invoices", "STAGING", Color(0xFF64748B))
                        PromptCardItem("v3.0-rc", "Support_Bot_Beta", "Testing new RAG injection", "A/B TEST 16%", Color(0xFFBE185D))
                    }
                }
            }

            // Live Performance Metrics Summary
            item {
                Card(
                    shape = RoundedCornerShape(16.dp),
                    colors = CardDefaults.cardColors(containerColor = cardBg),
                    modifier = Modifier.fillMaxWidth()
                ) {
                    Column(modifier = Modifier.padding(16.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                        Text("Live Performance", fontWeight = FontWeight.Bold, fontSize = 15.sp, color = textLight)

                        Row(modifier = Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween) {
                            Text("Safety Pass Rate", fontSize = 13.sp, color = Color.LightGray)
                            Text("99.9 %", fontWeight = FontWeight.Bold, fontSize = 14.sp, color = Color(0xFF10B981))
                        }
                        LinearProgressIndicator(progress = { 0.999f }, modifier = Modifier.fillMaxWidth(), color = Color(0xFF10B981))
                    }
                }
            }
        }
    }
}


@Composable
private fun PromptCardItem(version: String, title: String, desc: String, badge: String, badgeColor: Color) {
    Surface(
        shape = RoundedCornerShape(12.dp),
        color = Color(0xFF334155),
        modifier = Modifier.fillMaxWidth()
    ) {
        Row(
            modifier = Modifier.padding(12.dp).fillMaxWidth(),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically
        ) {
            Column(modifier = Modifier.weight(1f)) {
                Row(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text(version, fontWeight = FontWeight.Bold, fontSize = 13.sp, color = Color(0xFF818CF8))
                    Text(title, fontWeight = FontWeight.SemiBold, fontSize = 13.sp, color = Color.White)
                }
                Text(desc, fontSize = 11.sp, color = Color.LightGray)
            }
            Surface(shape = RoundedCornerShape(6.dp), color = badgeColor) {
                Text(badge, modifier = Modifier.padding(horizontal = 6.dp, vertical = 2.dp), fontSize = 9.sp, fontWeight = FontWeight.Bold, color = Color.White)
            }
        }
    }
}
